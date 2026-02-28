package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/translate"
)

type localTestInfra struct {
	proxyAddr  string
	mitmCACert []byte
	// upstream still needed for non-routed requests
	upstreamPort int
}

func setupLocalInfra(t *testing.T, resolver *config.ModelResolver) *localTestInfra {
	t.Helper()

	// Generate CAs
	upstreamCACert, upstreamCAKey, _ := testutil.GenerateTestCA()
	mitmCACert, mitmCAKey, _ := testutil.GenerateTestCA()
	serverCert, serverKey, _ := testutil.GenerateServerCert(upstreamCACert, upstreamCAKey, "localhost")

	// Start echo server (for upstream forwarding)
	echoSrv, echoPort, _ := testutil.NewEchoServer(serverCert, serverKey)
	t.Cleanup(func() { echoSrv.Close() })

	// Create MITM cert cache
	certCache, _ := mitm.NewCertCache(mitmCACert, mitmCAKey)

	// Create HTTP client that trusts the upstream CA
	upstreamPool := x509.NewCertPool()
	upstreamPool.AppendCertsFromPEM(upstreamCACert)
	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:  &tls.Config{RootCAs: upstreamPool},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	opts := []Option{
		WithHTTPClient(httpClient),
		WithModelResolver(resolver),
	}

	proxy := New(certCache, opts...)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &http.Server{Handler: proxy}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return &localTestInfra{
		proxyAddr:    ln.Addr().String(),
		mitmCACert:   mitmCACert,
		upstreamPort: echoPort,
	}
}

func localProxyRequest(t *testing.T, infra *localTestInfra, targetPort int, body []byte) (int, string, string) {
	t.Helper()

	targetHost := "localhost"
	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s:%d HTTP/1.1\r\nHost: %s\r\n\r\n", targetHost, targetPort, targetHost)

	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("CONNECT failed: %s", buf[:n])
	}

	mitmPool := x509.NewCertPool()
	mitmPool.AppendCertsFromPEM(infra.mitmCACert)
	tlsConn := tls.Client(conn, &tls.Config{
		RootCAs:    mitmPool,
		ServerName: targetHost,
	})
	tlsConn.Handshake()

	req := fmt.Sprintf("POST /v1/messages HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		targetHost, len(body))
	tlsConn.Write([]byte(req))
	tlsConn.Write(body)

	respData, _ := io.ReadAll(tlsConn)
	resp := string(respData)

	headerEnd := strings.Index(resp, "\r\n\r\n")
	if headerEnd == -1 {
		t.Fatalf("no header terminator")
	}

	statusLine := resp[:strings.Index(resp, "\r\n")]
	parts := strings.SplitN(statusLine, " ", 3)
	var statusCode int
	fmt.Sscanf(parts[1], "%d", &statusCode)

	// Extract content-type
	contentType := ""
	for _, line := range strings.Split(resp[:headerEnd], "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			contentType = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}

	return statusCode, resp[headerEnd+4:], contentType
}

func TestLocalRouteNonStreaming(t *testing.T) {
	// Start mock OpenAI server
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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, contentType := localProxyRequest(t, infra, infra.upstreamPort, body)

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
	// Model should be the label, not the backend name
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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
		"stream":     true,
	})

	status, respBody, contentType := localProxyRequest(t, infra, infra.upstreamPort, body)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected SSE content type, got %s", contentType)
	}

	// Verify Anthropic SSE event lifecycle
	for _, event := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(respBody, event) {
			t.Errorf("missing SSE event: %s", event)
		}
	}

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

	infra := setupLocalInfra(t, resolver)

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

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	var resp translate.AResponse
	if err := json.Unmarshal([]byte(respBody), &resp); err != nil {
		t.Fatalf("parse response: %v\nbody: %s", err, respBody)
	}

	// Should have a tool_use block
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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=unknown_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

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
	// Point to a port that's not listening
	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:     "dead",
			Endpoint: "http://127.0.0.1:1/v1", // nothing on port 1
			Models:   map[string]config.ModelConfig{"dead_model": {Model: "x"}},
		}},
	})

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=dead_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=broken_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

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
	// No resolver configured — should fall back to stub response
	infra := setupLocalInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	// Should get the stub response
	if !strings.Contains(respBody, "no local provider configured") {
		t.Error("expected stub response when no resolver configured")
	}
}

// capturingMockOpenAI starts a mock OpenAI server that captures the last request body.
func capturingMockOpenAI(t *testing.T) (port int, getLastReq func() []byte) {
	t.Helper()
	var mu sync.Mutex
	var lastBody []byte

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
		mu.Unlock()

		// Return a simple canned response
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
	}
}

func TestLocalRouteWithSchemaTransformComposed(t *testing.T) {
	oaiPort, getLastReq := capturingMockOpenAI(t)

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:      "mock",
			Endpoint:  fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Transform: []string{"schema:generic"},
			Models:    map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupLocalInfra(t, resolver)

	// Send request with tools containing fields that schema:generic should strip
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

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

	if status != 200 {
		t.Fatalf("expected 200, got %d: %s", status, respBody)
	}

	// Verify the request sent to the mock had schemas stripped
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
	// "type" and "properties" should still be present
	if _, exists := params["type"]; !exists {
		t.Error("type field should be preserved")
	}
	if _, exists := params["properties"]; !exists {
		t.Error("properties field should be preserved")
	}
}

func TestLocalRouteWithMultipleTransforms(t *testing.T) {
	oaiPort, getLastReq := capturingMockOpenAI(t)

	resolver, _ := config.NewModelResolver(&config.ProvidersConfig{
		Providers: []config.ProviderConfig{{
			Name:      "mock",
			Endpoint:  fmt.Sprintf("http://127.0.0.1:%d/v1", oaiPort),
			Transform: []string{"deepseek", "schema:generic"},
			Models:    map[string]config.ModelConfig{"test_model": {Model: "mock-model-v1"}},
		}},
	})

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "read a file"}},
		"max_tokens": 16384, // should be capped to 8192 by deepseek
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

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

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

	// Verify deepseek capped max_tokens
	maxTokens, ok := oaiReq["max_tokens"].(float64)
	if !ok {
		t.Fatal("max_tokens missing from captured request")
	}
	if maxTokens != 8192 {
		t.Errorf("expected max_tokens capped to 8192, got %v", maxTokens)
	}

	// Verify schema:generic stripped fields
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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
	})

	status, respBody, _ := localProxyRequest(t, infra, infra.upstreamPort, body)

	// Should NOT crash — should fall back to empty chain and process normally
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

func TestLocalRouteStreamTranslationError(t *testing.T) {
	// Mock server that returns HTTP 200 with SSE content-type but garbled data
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

	infra := setupLocalInfra(t, resolver)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=broken_model --> You are helpful",
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
		"max_tokens": 1024,
		"stream":     true,
	})

	status, respBody, contentType := localProxyRequest(t, infra, infra.upstreamPort, body)

	// The stream translator handles garbled data gracefully:
	// - Unparseable chunks are skipped (not a fatal error)
	// - TranslateStream returns nil (scanner.Err() is nil)
	// - We get a 200 SSE response with message_stop (even if no content was produced)
	// The key thing: we should get SOME response, not a silent close
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
