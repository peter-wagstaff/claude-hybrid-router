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
			Models:   map[string]string{"test_model": "mock-model-v1"},
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
			Models:   map[string]string{"test_model": "mock-model-v1"},
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
			Models:   map[string]string{"test_model": "mock-model-v1"},
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
			Models:   map[string]string{"known_model": "x"},
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
			Models:   map[string]string{"dead_model": "x"},
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
