package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/testutil"
)

type testInfra struct {
	proxyAddr    string
	upstreamPort int
	mitmCACert   []byte
}

// setupInfra creates a full proxy test stack: upstream echo server, MITM cert cache,
// and proxy. When resolver is non-nil, the proxy is configured with WithModelResolver.
func setupInfra(t *testing.T, resolver *config.ModelResolver) *testInfra {
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

	// Build proxy options
	opts := []Option{WithHTTPClient(httpClient)}
	if resolver != nil {
		opts = append(opts, WithModelResolver(resolver))
	}

	// Start proxy
	proxy := New(certCache, opts...)
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

// proxyRequest sends a request through the CONNECT proxy and returns status, body, and content-type.
func proxyRequest(t *testing.T, infra *testInfra, method, path string, body []byte, headers map[string]string) (int, string, string) {
	t.Helper()

	targetHost := "localhost"
	targetPort := infra.upstreamPort

	conn, err := net.Dial("tcp", infra.proxyAddr)
	if err != nil {
		t.Fatalf("connect to proxy: %v", err)
	}
	defer conn.Close()

	// Send CONNECT
	fmt.Fprintf(conn, "CONNECT %s:%d HTTP/1.1\r\nHost: %s\r\n\r\n",
		targetHost, targetPort, targetHost)

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
	var headerLines string
	for k, v := range headers {
		headerLines += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	if len(body) > 0 {
		headerLines += fmt.Sprintf("Content-Length: %d\r\n", len(body))
	}

	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\n%sConnection: close\r\n\r\n",
		method, path, targetHost, headerLines)
	tlsConn.Write([]byte(req))
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

	// Extract content-type
	contentType := ""
	for _, line := range strings.Split(resp[:headerEnd], "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			contentType = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
		}
	}

	return statusCode, resp[headerEnd+4:], contentType
}

// assertSSELifecycle checks that all 6 Anthropic SSE lifecycle events are present.
func assertSSELifecycle(t *testing.T, body string) {
	t.Helper()
	for _, event := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(body, event) {
			t.Errorf("missing SSE event: %s", event)
		}
	}
}
