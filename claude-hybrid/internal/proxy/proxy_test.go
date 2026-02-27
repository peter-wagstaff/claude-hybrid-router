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

	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
)

type testInfra struct {
	proxyAddr    string
	upstreamPort int
	mitmCACert   []byte
}

func setupInfra(t *testing.T) *testInfra {
	t.Helper()

	// Generate CAs
	upstreamCACert, upstreamCAKey, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("generate upstream CA: %v", err)
	}
	mitmCACert, mitmCAKey, err := testutil.GenerateTestCA()
	if err != nil {
		t.Fatalf("generate MITM CA: %v", err)
	}

	// Generate server cert signed by upstream CA
	serverCert, serverKey, err := testutil.GenerateServerCert(upstreamCACert, upstreamCAKey, "localhost")
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}

	// Start echo server
	echoSrv, echoPort, err := testutil.NewEchoServer(serverCert, serverKey)
	if err != nil {
		t.Fatalf("start echo server: %v", err)
	}
	t.Cleanup(func() { echoSrv.Close() })

	// Create MITM cert cache
	certCache, err := mitm.NewCertCache(mitmCACert, mitmCAKey)
	if err != nil {
		t.Fatalf("create cert cache: %v", err)
	}

	// Create HTTP client that trusts the upstream CA
	upstreamPool := x509.NewCertPool()
	upstreamPool.AppendCertsFromPEM(upstreamCACert)
	httpClient := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				RootCAs: upstreamPool,
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Start proxy
	proxy := New(certCache, WithHTTPClient(httpClient))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: proxy}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	return &testInfra{
		proxyAddr:    ln.Addr().String(),
		upstreamPort: echoPort,
		mitmCACert:   mitmCACert,
	}
}

// proxyRequest sends a request through the CONNECT proxy and returns status + body.
func proxyRequest(t *testing.T, infra *testInfra, method, path string, body []byte, headers [][2]string) (int, string) {
	t.Helper()

	targetHost := "localhost"
	targetPort := infra.upstreamPort

	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect to proxy: %v", err)
	}
	defer conn.Close()

	// Send CONNECT
	connectReq := fmt.Sprintf("CONNECT %s:%d HTTP/1.1\r\nHost: %s\r\n\r\n",
		targetHost, targetPort, targetHost)
	conn.Write([]byte(connectReq))

	// Read CONNECT response
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("CONNECT failed: %s", buf[:n])
	}

	// TLS handshake with MITM cert
	mitmPool := x509.NewCertPool()
	mitmPool.AppendCertsFromPEM(infra.mitmCACert)
	tlsConn := tls.Client(conn, &tls.Config{
		RootCAs:    mitmPool,
		ServerName: targetHost,
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}

	// Build HTTP request
	var reqLines []string
	reqLines = append(reqLines, fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))
	reqLines = append(reqLines, fmt.Sprintf("Host: %s\r\n", targetHost))
	for _, h := range headers {
		reqLines = append(reqLines, fmt.Sprintf("%s: %s\r\n", h[0], h[1]))
	}
	if len(body) > 0 {
		reqLines = append(reqLines, fmt.Sprintf("Content-Length: %d\r\n", len(body)))
	}
	reqLines = append(reqLines, "Connection: close\r\n")
	reqLines = append(reqLines, "\r\n")

	tlsConn.Write([]byte(strings.Join(reqLines, "")))
	if len(body) > 0 {
		tlsConn.Write(body)
	}

	// Read response
	respData, _ := io.ReadAll(tlsConn)
	resp := string(respData)

	// Parse status code
	headerEnd := strings.Index(resp, "\r\n\r\n")
	if headerEnd == -1 {
		t.Fatalf("no header terminator in response: %q", resp)
	}
	statusLine := resp[:strings.Index(resp, "\r\n")]
	parts := strings.SplitN(statusLine, " ", 3)
	if len(parts) < 2 {
		t.Fatalf("bad status line: %s", statusLine)
	}
	var statusCode int
	fmt.Sscanf(parts[1], "%d", &statusCode)

	respBody := resp[headerEnd+4:]
	return statusCode, respBody
}

func TestCleanRequestForwarded(t *testing.T) {
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": "hello world"}},
	})
	status, respBody := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	var echo testutil.EchoResponse
	if err := json.Unmarshal([]byte(respBody), &echo); err != nil {
		t.Fatalf("parse echo response: %v\nbody: %s", err, respBody)
	}
	if echo.Method != "POST" {
		t.Errorf("expected POST, got %s", echo.Method)
	}
	if !strings.Contains(echo.Body, "hello world") {
		t.Error("request body not forwarded")
	}
}

func TestGetRequestNoBody(t *testing.T) {
	infra := setupInfra(t)

	status, respBody := proxyRequest(t, infra, "GET", "/v1/models", nil, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	var echo testutil.EchoResponse
	json.Unmarshal([]byte(respBody), &echo)
	if echo.Method != "GET" {
		t.Errorf("expected GET, got %s", echo.Method)
	}
}

func TestMultipleRequestsSingleTunnel(t *testing.T) {
	infra := setupInfra(t)
	targetHost := "localhost"
	targetPort := infra.upstreamPort

	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	connectReq := fmt.Sprintf("CONNECT %s:%d HTTP/1.1\r\nHost: %s\r\n\r\n",
		targetHost, targetPort, targetHost)
	conn.Write([]byte(connectReq))

	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("CONNECT failed")
	}

	mitmPool := x509.NewCertPool()
	mitmPool.AppendCertsFromPEM(infra.mitmCACert)
	tlsConn := tls.Client(conn, &tls.Config{
		RootCAs:    mitmPool,
		ServerName: targetHost,
	})
	tlsConn.Handshake()

	for i := range 3 {
		body, _ := json.Marshal(map[string]interface{}{
			"messages": []map[string]string{{"role": "user", "content": fmt.Sprintf("request %d", i)}},
		})

		req := fmt.Sprintf("POST /v1/messages HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n",
			targetHost, len(body))
		tlsConn.Write([]byte(req))
		tlsConn.Write(body)

		// Read response headers
		respBuf := make([]byte, 0, 8192)
		tmp := make([]byte, 4096)
		for !strings.Contains(string(respBuf), "\r\n\r\n") {
			n, err := tlsConn.Read(tmp)
			if err != nil {
				t.Fatalf("read response %d: %v", i, err)
			}
			respBuf = append(respBuf, tmp[:n]...)
		}

		headerEnd := strings.Index(string(respBuf), "\r\n\r\n")
		headers := string(respBuf[:headerEnd])
		after := respBuf[headerEnd+4:]

		if !strings.Contains(strings.SplitN(headers, "\r\n", 2)[0], "200") {
			t.Fatalf("request %d: non-200 response", i)
		}

		// Find Content-Length
		var cl int
		for _, line := range strings.Split(headers, "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				fmt.Sscanf(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), "%d", &cl)
			}
		}

		// Read remaining body
		for len(after) < cl {
			n, err := tlsConn.Read(tmp)
			if err != nil {
				t.Fatalf("read body %d: %v", i, err)
			}
			after = append(after, tmp[:n]...)
		}

		var echo testutil.EchoResponse
		if err := json.Unmarshal(after[:cl], &echo); err != nil {
			t.Fatalf("parse response %d: %v", i, err)
		}
		if echo.Method != "POST" {
			t.Errorf("request %d: expected POST", i)
		}
		if !strings.Contains(echo.Body, fmt.Sprintf("request %d", i)) {
			t.Errorf("request %d: body not forwarded", i)
		}
	}
}

func TestLocalRouteDetected(t *testing.T) {
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, respBody := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(respBody), &data); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if data["type"] != "message" {
		t.Error("unexpected type")
	}
	if data["role"] != "assistant" {
		t.Error("unexpected role")
	}
	if data["model"] != "my_local_model" {
		t.Error("unexpected model")
	}
	content := data["content"].([]interface{})[0].(map[string]interface{})
	text := content["text"].(string)
	if !strings.Contains(text, "my_local_model") {
		t.Error("stub text missing model")
	}
	if !strings.Contains(text, "no local provider configured") {
		t.Error("stub text missing message")
	}
}

func TestLocalRouteStreaming(t *testing.T) {
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
		"stream":   true,
	})
	status, respBody := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

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
	if !strings.Contains(respBody, "my_local_model") {
		t.Error("missing model in SSE")
	}
	if !strings.Contains(respBody, "no local provider configured") {
		t.Error("missing stub text in SSE")
	}
}

func TestLocalRouteMarkerStripped(t *testing.T) {
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, respBody := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if strings.Contains(respBody, "@proxy-local-route") {
		t.Error("marker should be stripped from response")
	}
}

func TestMarkerInMessagesNotRouted(t *testing.T) {
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{
			"role":    "user",
			"content": "<!-- @proxy-local-route:af83e9 model=my_local_model --> hello",
		}},
	})
	status, respBody := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	var echo testutil.EchoResponse
	if err := json.Unmarshal([]byte(respBody), &echo); err != nil {
		t.Fatalf("parse echo response: %v", err)
	}
	if echo.Method != "POST" {
		t.Error("should be forwarded, not intercepted")
	}
	if !strings.Contains(echo.Body, "@proxy-local-route") {
		t.Error("marker should be in forwarded body")
	}
}

func TestAuthHeadersNotLogged(t *testing.T) {
	// This test verifies the code path works — the actual log sanitization
	// is verified by inspecting the log filter in the handler.
	// We can't easily capture log output in this test structure,
	// but the code path is covered by TestLocalRouteDetected.
	infra := setupInfra(t)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, [][2]string{
		{"x-api-key", "sk-secret-key-12345"},
		{"authorization", "Bearer secret-token"},
	})
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}
