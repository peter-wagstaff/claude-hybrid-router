package testutil

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// EchoResponse is the JSON structure returned by the echo server.
type EchoResponse struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// NewEchoServer starts an HTTPS echo server and returns it along with its port.
// The server uses the provided cert/key PEM bytes.
func NewEchoServer(certPEM, keyPEM []byte) (*http.Server, int, error) {
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, 0, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := make(map[string]string)
		for k := range r.Header {
			headers[k] = r.Header.Get(k)
		}
		resp := EchoResponse{
			Method:  r.Method,
			Path:    r.URL.RequestURI(),
			Headers: headers,
			Body:    string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			fmt.Printf("echo server error: %v\n", err)
		}
	}()

	return srv, port, nil
}
