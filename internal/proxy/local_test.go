package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/translate"
)

// capturingMockOpenAI starts a mock OpenAI server that captures the last request body and headers.
func capturingMockOpenAI(t *testing.T) (port int, getLastBody func() []byte, getLastHeaders func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var lastBody []byte
	var lastHeaders http.Header

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port = ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		lastBody = body
		lastHeaders = r.Header.Clone()
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "chatcmpl-mock",
			"model": "captured",
			"choices": []map[string]interface{}{{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			},
		})
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return port, func() []byte {
			mu.Lock()
			defer mu.Unlock()
			return lastBody
		}, func() http.Header {
			mu.Lock()
			defer mu.Unlock()
			return lastHeaders
		}
}

func TestLocalRouteNonStreaming(t *testing.T) {
	oaiSrv, oaiPort, err := testutil.MockOpenAIServer()
	if err != nil {
		t.Fatalf("mock openai: %v", err)
	}
	t.Cleanup(func() { oaiSrv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "mock",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Models:   map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, contentType := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("expected JSON content type, got %s", contentType)
	}

	var resp translate.AResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, respBody)
	}

	if resp.Type != "message" {
		t.Errorf("expected type message, got %s", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", resp.Role)
	}
	if resp.Model != "test_model" {
		t.Errorf("expected model label 'test_model', got %s", resp.Model)
	}
	if len(resp.Content) == 0 {
		t.Fatal("empty content")
	}
	if resp.Content[0].Type != "text" {
		t.Errorf("expected text block, got %s", resp.Content[0].Type)
	}
	if !strings.Contains(resp.Content[0].Text, "mock-model-v1") {
		t.Errorf("response should mention backend model: %s", resp.Content[0].Text)
	}
	if *resp.StopReason != "end_turn" {
		t.Errorf("expected stop_reason end_turn, got %s", *resp.StopReason)
	}
}

func TestLocalRouteStreamingTranslation(t *testing.T) {
	oaiSrv, oaiPort, _ := testutil.MockOpenAIServer()
	t.Cleanup(func() { oaiSrv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "mock",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Models:   map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
		"stream":     true,
	})

	status, respBody, contentType := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected SSE content type, got %s", contentType)
	}

	assertSSELifecycle(t, respBody)

	if !strings.Contains(respBody, "test_model") {
		t.Error("model label missing from SSE")
	}
	if !strings.Contains(respBody, `"stop_reason":"end_turn"`) {
		t.Error("missing end_turn stop_reason")
	}
}

func TestLocalRouteToolUse(t *testing.T) {
	oaiSrv, oaiPort, _ := testutil.MockOpenAIServer()
	t.Cleanup(func() { oaiSrv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "mock",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Models:   map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "read a file"}},
		"max_tokens": 1024,
		"tools": []map[string]interface{}{{
			"name":         "Read",
			"description":  "Read a file",
			"input_schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"file_path": map[string]string{"type": "string"}}},
		}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	var resp translate.AResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, respBody)
	}

	foundToolUse := false
	for _, block := range resp.Content {
		if block.Type == "tool_use" {
			foundToolUse = true
			if block.Name != "Read" {
				t.Errorf("expected tool name Read, got %s", block.Name)
			}
			var input map[string]string
			json.Unmarshal(block.Input, &input)
			if input["file_path"] != "/tmp/test.txt" {
				t.Errorf("unexpected input: %v", input)
			}
		}
	}
	if !foundToolUse {
		t.Error("expected tool_use block in response")
	}
	if *resp.StopReason != "tool_use" {
		t.Errorf("expected stop_reason tool_use, got %s", *resp.StopReason)
	}
}

func TestLocalRouteUnknownModel(t *testing.T) {
	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "mock",
			Endpoint: "http://127.0.0.1:1/v1",
			Models:   map[string]config.ModelConfig{"known_model": {Model: "x"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=unknown_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 400 {
		t.Fatalf("expected 400, got %d", status)
	}

	var errResp translate.AErrorResponse
	if err := json.Unmarshal([]byte(respBody), &errResp); err != nil {
		t.Fatalf("parse error response: %v\nbody: %s", err, respBody)
	}
	if errResp.Type != "error" {
		t.Errorf("expected type error, got %s", errResp.Type)
	}
	if !strings.Contains(errResp.Error.Message, "unknown_model") {
		t.Errorf("error should mention the model label: %s", errResp.Error.Message)
	}
}

func TestLocalRouteProviderDown(t *testing.T) {
	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "dead",
			Endpoint: "http://127.0.0.1:1/v1",
			Models:   map[string]config.ModelConfig{"dead_model": {Model: "x"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=dead_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 502 {
		t.Fatalf("expected 502, got %d", status)
	}

	var errResp translate.AErrorResponse
	json.Unmarshal([]byte(respBody), &errResp)
	if errResp.Type != "error" {
		t.Errorf("expected error type, got %s", errResp.Type)
	}
	if !strings.Contains(errResp.Error.Message, "dead_model") {
		t.Errorf("error should mention model label: %s", errResp.Error.Message)
	}
}

func TestLocalRouteResponseReadError(t *testing.T) {
	// Start a server that sends an incomplete response body (triggers read error)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			conn.Read(buf)
			conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 10000\r\n\r\n{\"partial"))
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "broken",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			Models:   map[string]config.ModelConfig{"broken_model": {Model: "x"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=broken_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 502 {
		t.Fatalf("expected 502, got %d: %s", status, respBody)
	}

	var errResp translate.AErrorResponse
	json.Unmarshal([]byte(respBody), &errResp)
	if errResp.Type != "error" {
		t.Errorf("expected error type, got %s", errResp.Type)
	}
}

func TestLocalRouteNoResolverFallsBackToStub(t *testing.T) {
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	if !strings.Contains(respBody, "no local provider configured") {
		t.Error("expected stub response when no resolver configured")
	}
}

func TestLocalRouteWithSchemaTransformComposed(t *testing.T) {
	oaiPort, getLastReq, _ := capturingMockOpenAI(t)

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:      "mock",
			Endpoint:  fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Transform: []string{"schema:generic"},
			Models:    map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "read a file"}},
		"max_tokens": 1024,
		"tools": []map[string]interface{}{{
			"name":        "Read",
			"description": "Read a file",
			"input_schema": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"strict":               true,
				"$schema":              "http://json-schema.org/draft-07/schema#",
				"properties": map[string]interface{}{
					"file_path": map[string]string{"type": "string"},
				},
			},
		}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	captured := getLastReq()
	if len(captured) == 0 {
		t.Fatal("mock server did not receive a request")
	}

	var oaiReq map[string]interface{}
	if err := json.Unmarshal(captured, &oaiReq); err != nil {
		t.Fatalf("parse captured request: %v", err)
	}

	tools, ok := oaiReq["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		t.Fatal("no tools in captured request")
	}

	tool := tools[0].(map[string]interface{})
	fn := tool["function"].(map[string]interface{})
	params := fn["parameters"].(map[string]interface{})

	if _, exists := params["additionalProperties"]; exists {
		t.Error("additionalProperties should have been stripped by schema:generic")
	}
	if _, exists := params["strict"]; exists {
		t.Error("strict should have been stripped by schema:generic")
	}
	if _, exists := params["$schema"]; exists {
		t.Error("$schema should have been stripped by schema:generic")
	}
	if _, exists := params["type"]; !exists {
		t.Error("type field should be preserved")
	}
	if _, exists := params["properties"]; !exists {
		t.Error("properties field should be preserved")
	}
}

func TestLocalRouteWithMultipleTransforms(t *testing.T) {
	oaiPort, getLastReq, _ := capturingMockOpenAI(t)

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:      "mock",
			Endpoint:  fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Transform: []string{"deepseek", "schema:generic"},
			Models:    map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "read a file"}},
		"max_tokens": 16384,
		"tools": []map[string]interface{}{{
			"name":        "Read",
			"description": "Read a file",
			"input_schema": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"$schema":              "http://json-schema.org/draft-07/schema#",
				"properties": map[string]interface{}{
					"file_path": map[string]string{"type": "string"},
				},
			},
		}},
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	captured := getLastReq()
	if len(captured) == 0 {
		t.Fatal("mock server did not receive a request")
	}

	var oaiReq map[string]interface{}
	if err := json.Unmarshal(captured, &oaiReq); err != nil {
		t.Fatalf("parse captured request: %v", err)
	}

	// deepseek transform renames max_completion_tokens → max_tokens
	maxTokens, ok := oaiReq["max_tokens"].(float64)
	if !ok {
		t.Fatal("max_tokens missing from captured request (deepseek transform should rename)")
	}
	if maxTokens != 16384 {
		t.Errorf("expected max_tokens=16384 (passthrough, no cap), got %v", maxTokens)
	}
	if _, exists := oaiReq["max_completion_tokens"]; exists {
		t.Error("max_completion_tokens should be removed by deepseek transform")
	}

	tools := oaiReq["tools"].([]interface{})
	tool := tools[0].(map[string]interface{})
	fn := tool["function"].(map[string]interface{})
	params := fn["parameters"].(map[string]interface{})

	if _, exists := params["additionalProperties"]; exists {
		t.Error("additionalProperties should have been stripped by schema:generic")
	}
	if _, exists := params["$schema"]; exists {
		t.Error("$schema should have been stripped by schema:generic")
	}
}

func TestLocalRouteWithUnknownTransform(t *testing.T) {
	oaiSrv, oaiPort, err := testutil.MockOpenAIServer()
	if err != nil {
		t.Fatalf("mock openai: %v", err)
	}
	t.Cleanup(func() { oaiSrv.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:      "mock",
			Endpoint:  fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Transform: []string{"nonexistent"},
			Models:    map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200 (graceful fallback), got %d: %s", status, respBody)
	}

	var resp translate.AResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, respBody)
	}

	if resp.Type != "message" {
		t.Errorf("expected type message, got %s", resp.Type)
	}
	if resp.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", resp.Role)
	}
	if len(resp.Content) == 0 {
		t.Fatal("empty content")
	}
	if !strings.Contains(resp.Content[0].Text, "mock-model-v1") {
		t.Errorf("response should mention backend model: %s", resp.Content[0].Text)
	}
}

func TestLocalRouteDoesNotLeakAuthHeaders(t *testing.T) {
	oaiPort, _, getLastHeaders := capturingMockOpenAI(t)

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "mock",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			APIKey:   "provider-secret-key",
			Models:   map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, map[string]string{
		"x-api-key":         "sk-ant-CLAUDE_SECRET_KEY",
		"Authorization":     "Bearer sk-ant-CLAUDE_SECRET_KEY",
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    "max-tokens-3-5-sonnet-2024-07-15",
	})

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	headers := getLastHeaders()
	if headers == nil {
		t.Fatal("mock server did not receive a request")
	}

	if v := headers.Get("X-Api-Key"); v != "" {
		t.Errorf("x-api-key leaked to local provider: %s", v)
	}

	if auth := headers.Get("Authorization"); auth != "" {
		if strings.Contains(auth, "sk-ant-") {
			t.Errorf("Claude's Authorization header leaked to local provider: %s", auth)
		}
		if auth != "Bearer provider-secret-key" {
			t.Errorf("expected provider API key, got: %s", auth)
		}
	}

	if v := headers.Get("Anthropic-Version"); v != "" {
		t.Errorf("anthropic-version leaked to local provider: %s", v)
	}
	if v := headers.Get("Anthropic-Beta"); v != "" {
		t.Errorf("anthropic-beta leaked to local provider: %s", v)
	}

	if ct := headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got: %s", ct)
	}
}

func TestLocalRouteStreamTranslationError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			conn.Read(buf)
			resp := "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n" +
				"data: {not valid json at all\n\n" +
				"data: {also broken\n\n" +
				"data: [DONE]\n\n"
			conn.Write([]byte(resp))
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "broken",
			Endpoint: fmt.Sprintf("http://127.0.0.1:%d/v1", port),
			Models:   map[string]config.ModelConfig{"broken_model": {Model: "x"}},
		}},
	})

	infra := setupInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=broken_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
		"stream":     true,
	})

	status, respBody, contentType := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected SSE content type, got %s", contentType)
	}
	if !strings.Contains(respBody, "message_stop") {
		t.Error("expected message_stop event in response")
	}
}
