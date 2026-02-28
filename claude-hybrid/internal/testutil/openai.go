package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// OpenAIChoice mirrors an OpenAI chat completion choice.
type OpenAIChoice struct {
	Message struct {
		Role      string          `json:"role"`
		Content   string          `json:"content,omitempty"`
		ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// MockOpenAIServer starts a mock OpenAI-compatible chat completions server.
// It echoes back the request model and a canned response.
// For streaming requests, it returns SSE chunks.
func MockOpenAIServer() (*http.Server, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	return srv, port, nil
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
		Stream   bool            `json:"stream"`
		Tools    json.RawMessage `json:"tools,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}

	// Check if tools are present — if so, return a tool call response
	hasTools := len(req.Tools) > 0 && string(req.Tools) != "null"

	if req.Stream {
		handleStreaming(w, req.Model, hasTools)
		return
	}

	handleNonStreaming(w, req.Model, hasTools)
}

func handleNonStreaming(w http.ResponseWriter, model string, hasTools bool) {
	var resp map[string]interface{}

	if hasTools {
		resp = map[string]interface{}{
			"id":    "chatcmpl-mock",
			"model": model,
			"choices": []map[string]interface{}{{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]interface{}{{
						"id":   "call_mock_123",
						"type": "function",
						"function": map[string]string{
							"name":      "Read",
							"arguments": `{"file_path": "/tmp/test.txt"}`,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]int{
				"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
			},
		}
	} else {
		resp = map[string]interface{}{
			"id":    "chatcmpl-mock",
			"model": model,
			"choices": []map[string]interface{}{{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": fmt.Sprintf("Mock response from %s", model),
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleStreaming(w http.ResponseWriter, model string, hasTools bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	writeSSEChunk := func(data string) {
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if hasTools {
		// Tool call streaming
		chunks := []string{
			// Tool call start
			mustJSON(map[string]interface{}{
				"id": "chatcmpl-mock",
				"choices": []map[string]interface{}{{
					"delta": map[string]interface{}{
						"role": "assistant",
						"tool_calls": []map[string]interface{}{{
							"index": 0,
							"id":    "call_mock_stream",
							"type":  "function",
							"function": map[string]string{
								"name":      "Read",
								"arguments": "",
							},
						}},
					},
					"finish_reason": nil,
				}},
			}),
			// Argument fragment
			mustJSON(map[string]interface{}{
				"id": "chatcmpl-mock",
				"choices": []map[string]interface{}{{
					"delta": map[string]interface{}{
						"tool_calls": []map[string]interface{}{{
							"index":    0,
							"function": map[string]string{"arguments": `{"file_path":"/tmp/test.txt"}`},
						}},
					},
					"finish_reason": nil,
				}},
			}),
			// Finish
			mustJSON(map[string]interface{}{
				"id": "chatcmpl-mock",
				"choices": []map[string]interface{}{{
					"delta":         map[string]interface{}{},
					"finish_reason": "tool_calls",
				}},
			}),
		}
		for _, c := range chunks {
			writeSSEChunk(c)
		}
	} else {
		// Text streaming
		words := strings.Fields(fmt.Sprintf("Mock streaming response from %s", model))
		for _, word := range words {
			c := mustJSON(map[string]interface{}{
				"id": "chatcmpl-mock",
				"choices": []map[string]interface{}{{
					"delta":         map[string]interface{}{"content": word + " "},
					"finish_reason": nil,
				}},
			})
			writeSSEChunk(c)
		}
		// Final chunk with finish_reason
		writeSSEChunk(mustJSON(map[string]interface{}{
			"id": "chatcmpl-mock",
			"choices": []map[string]interface{}{{
				"delta":         map[string]interface{}{},
				"finish_reason": "stop",
			}},
		}))
	}

	writeSSEChunk("[DONE]")
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
