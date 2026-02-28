package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// groqTransform strips cache_control, $schema from requests and fixes numeric tool call IDs.
type groqTransform struct{}

func newGroqTransform() *groqTransform {
	return &groqTransform{}
}

func (g *groqTransform) Name() string { return "groq" }

// TransformRequest strips cache_control from messages and $schema from tool parameters.
func (g *groqTransform) TransformRequest(req map[string]interface{}, ctx *TransformContext) error {
	// Strip cache_control from messages.
	if messages, ok := req["messages"].([]interface{}); ok {
		for _, m := range messages {
			msg, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			delete(msg, "cache_control")
			if content, ok := msg["content"].([]interface{}); ok {
				for _, c := range content {
					if part, ok := c.(map[string]interface{}); ok {
						delete(part, "cache_control")
					}
				}
			}
		}
	}

	// Strip $schema from tool parameters.
	if tools, ok := req["tools"].([]interface{}); ok {
		for _, t := range tools {
			tool, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := tool["function"].(map[string]interface{})
			if !ok {
				continue
			}
			params, ok := fn["parameters"].(map[string]interface{})
			if !ok {
				continue
			}
			delete(params, "$schema")
		}
	}

	return nil
}

// TransformResponse is a passthrough.
func (g *groqTransform) TransformResponse(body []byte, ctx *TransformContext) ([]byte, error) {
	return body, nil
}

// TransformStreamChunk fixes numeric tool call IDs and adjusts choice index.
func (g *groqTransform) TransformStreamChunk(data []byte, ctx *TransformContext) ([][]byte, error) {
	var chunk map[string]interface{}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return [][]byte{data}, nil
	}

	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return [][]byte{data}, nil
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return [][]byte{data}, nil
	}
	toolCalls, ok := delta["tool_calls"].([]interface{})
	if !ok || len(toolCalls) == 0 {
		return [][]byte{data}, nil
	}

	modified := false

	// Fix numeric tool call ID.
	if tc, ok := toolCalls[0].(map[string]interface{}); ok {
		if id, ok := tc["id"].(string); ok {
			if _, err := strconv.Atoi(id); err == nil {
				tc["id"] = "call_" + groqRandomHex(12)
				modified = true
			}
		}
	}

	// Adjust index if there's prior text content.
	if ctx.HasTextContent {
		idx, _ := choice["index"].(float64)
		choice["index"] = idx + 1
		modified = true
	}

	if !modified {
		return [][]byte{data}, nil
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return [][]byte{data}, nil
	}
	return [][]byte{out}, nil
}

// groqRandomHex generates n random hex bytes (2n hex chars).
func groqRandomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func init() {
	RegisterTransform("groq", func() Transformer {
		return newGroqTransform()
	})
}
