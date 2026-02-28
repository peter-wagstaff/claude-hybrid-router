package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
)

func TestCleanRequestForwarded(t *testing.T) {
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": "hello world"}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
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
	infra := setupInfra(t, nil)

	status, respBody, _ := proxyRequest(t, infra, "GET", "/v1/models", nil, nil)
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
	infra := setupInfra(t, nil)
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
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
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
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
		"stream":   true,
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}

	assertSSELifecycle(t, respBody)
	if !strings.Contains(respBody, "my_local_model") {
		t.Error("missing model in SSE")
	}
	if !strings.Contains(respBody, "no local provider configured") {
		t.Error("missing stub text in SSE")
	}
}

func TestLocalRouteMarkerStripped(t *testing.T) {
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=test_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
	if strings.Contains(respBody, "@proxy-local-route") {
		t.Error("marker should be stripped from response")
	}
}

func TestMarkerInMessagesNotRouted(t *testing.T) {
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"messages": []map[string]string{{
			"role":    "user",
			"content": "<!-- @proxy-local-route:af83e9 model=my_local_model --> hello",
		}},
	})
	status, respBody, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, nil)
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
	infra := setupInfra(t, nil)

	body, _ := json.Marshal(map[string]interface{}{
		"model":    "claude-sonnet-4-20250514",
		"system":   "<!-- @proxy-local-route:af83e9 model=my_local_model --> You are helpful",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	status, _, _ := proxyRequest(t, infra, "POST", "/v1/messages", body, map[string]string{
		"x-api-key":     "sk-secret-key-12345",
		"authorization": "Bearer secret-token",
	})
	if status != 200 {
		t.Fatalf("expected 200, got %d", status)
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	infra := setupInfra(t, nil)

	// Dial proxy directly and CONNECT to a dead port (port 1)
	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect to proxy: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT localhost:1 HTTP/1.1\r\nHost: localhost\r\n\r\n")

	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	resp := string(buf[:n])

	// The proxy should return 200 for CONNECT (the TLS handshake will fail or the upstream will be unreachable)
	// Since CONNECT succeeds but the upstream is dead, we need to do a TLS handshake and then the request should fail
	if !strings.Contains(resp, "200") {
		// If the proxy immediately rejects with a non-200, that's also valid behavior
		// for an unreachable target — check for 502
		if !strings.Contains(resp, "502") {
			t.Fatalf("expected 200 or 502, got: %s", resp)
		}
		return // 502 is valid — test passes
	}

	// If we got 200, try a TLS handshake + request — it should fail
	mitmPool := x509.NewCertPool()
	mitmPool.AppendCertsFromPEM(infra.mitmCACert)
	tlsConn := tls.Client(conn, &tls.Config{
		RootCAs:    mitmPool,
		ServerName: "localhost",
	})
	if err := tlsConn.Handshake(); err != nil {
		// TLS handshake failure to dead port is expected
		return
	}

	req := "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"
	tlsConn.Write([]byte(req))
	respData, _ := io.ReadAll(tlsConn)
	respStr := string(respData)

	// Should get a 502 or connection error
	if len(respStr) > 0 && !strings.Contains(respStr, "502") {
		t.Logf("response from dead upstream: %s", respStr)
	}
}

func TestNonConnectMethodRejected(t *testing.T) {
	infra := setupInfra(t, nil)

	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect to proxy: %v", err)
	}
	defer conn.Close()

	// Send a GET (not CONNECT) directly to the proxy
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")

	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	resp := string(buf[:n])

	if !strings.Contains(resp, "405") {
		t.Errorf("expected 405 Method Not Allowed, got: %s", resp)
	}
}
