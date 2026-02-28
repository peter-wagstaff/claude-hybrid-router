// integration-test sends a request through the proxy to a real OpenAI-compatible provider.
// Usage: go run ./cmd/integration-test -proxy localhost:PORT [-stream]
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	proxyAddr := flag.String("proxy", "", "proxy address (host:port)")
	stream := flag.Bool("stream", false, "use streaming")
	flag.Parse()

	if *proxyAddr == "" {
		fmt.Fprintln(os.Stderr, "usage: integration-test -proxy HOST:PORT [-stream]")
		os.Exit(1)
	}

	// Load MITM CA cert
	home, _ := os.UserHomeDir()
	caCert, err := os.ReadFile(filepath.Join(home, ".claude-hybrid", "certs", "ca.crt"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read CA cert: %v\n", err)
		os.Exit(1)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCert)

	// Connect to proxy
	conn, err := net.Dial("tcp", *proxyAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// CONNECT to Anthropic's API (the proxy will MITM this)
	fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n")
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "200") {
		fmt.Fprintf(os.Stderr, "CONNECT failed: %s\n", buf[:n])
		os.Exit(1)
	}

	// TLS handshake (trusting our MITM CA)
	tlsConn := tls.Client(conn, &tls.Config{
		RootCAs:    pool,
		ServerName: "api.anthropic.com",
	})
	if err := tlsConn.Handshake(); err != nil {
		fmt.Fprintf(os.Stderr, "TLS handshake: %v\n", err)
		os.Exit(1)
	}
	defer tlsConn.Close()

	// Build Anthropic-format request with routing marker
	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"system":     "<!-- @proxy-local-route:af83e9 model=test_model --> You are a helpful assistant. Reply in one sentence.",
		"messages":   []map[string]string{{"role": "user", "content": "What is 2+2?"}},
		"max_tokens": 256,
		"stream":     *stream,
	})

	req := fmt.Sprintf("POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(body))
	tlsConn.Write([]byte(req))
	tlsConn.Write(body)

	// Read response
	resp, _ := io.ReadAll(tlsConn)
	response := string(resp)

	// Split headers and body
	headerEnd := strings.Index(response, "\r\n\r\n")
	if headerEnd == -1 {
		fmt.Fprintln(os.Stderr, "no header terminator in response")
		os.Exit(1)
	}

	headers := response[:headerEnd]
	respBody := response[headerEnd+4:]

	fmt.Println("=== HEADERS ===")
	fmt.Println(headers)
	fmt.Println()
	fmt.Println("=== BODY ===")
	fmt.Println(respBody)

	// Quick validation
	if strings.Contains(headers, "200") {
		fmt.Println("\n✓ Got 200 OK")
	} else {
		fmt.Println("\n✗ Non-200 response")
		os.Exit(1)
	}

	if *stream {
		if strings.Contains(respBody, "event: message_start") {
			fmt.Println("✓ Has message_start event")
		} else {
			fmt.Println("✗ Missing message_start event")
		}
		if strings.Contains(respBody, "event: message_stop") {
			fmt.Println("✓ Has message_stop event")
		} else {
			fmt.Println("✗ Missing message_stop event")
		}
		if strings.Contains(respBody, "event: content_block_delta") {
			fmt.Println("✓ Has content_block_delta events")
		} else {
			fmt.Println("✗ Missing content_block_delta events")
		}
	} else {
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(respBody), &msg); err == nil {
			fmt.Printf("✓ Valid JSON, type=%v, model=%v\n", msg["type"], msg["model"])
		} else {
			fmt.Printf("✗ Invalid JSON: %v\n", err)
		}
	}
}
