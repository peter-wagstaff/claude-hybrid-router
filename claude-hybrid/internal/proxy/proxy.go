// Package proxy implements the MITM CONNECT proxy with local model routing.
package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
)

// Proxy is an HTTP handler that handles CONNECT requests with MITM TLS.
type Proxy struct {
	certCache  *mitm.CertCache
	httpClient *http.Client
	sem        chan struct{}
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithHTTPClient sets a custom HTTP client for upstream requests.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Proxy) { p.httpClient = c }
}

// New creates a new Proxy.
func New(cache *mitm.CertCache, opts ...Option) *Proxy {
	p := &Proxy{
		certCache: cache,
		sem:       make(chan struct{}, config.MaxProxyGoroutines),
	}
	for _, o := range opts {
		o(p)
	}
	if p.httpClient == nil {
		p.httpClient = &http.Client{
			Transport: &http.Transport{
				ForceAttemptHTTP2: true,
				TLSClientConfig:  &tls.Config{},
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: config.UpstreamTimeout,
		}
	}
	return p
}

// ServeHTTP handles CONNECT requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT supported", http.StatusMethodNotAllowed)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}

	// Acquire semaphore (non-blocking)
	select {
	case p.sem <- struct{}{}:
	default:
		http.Error(w, "proxy overloaded", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-p.sem }()

	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		log.Printf("hijack error: %v", err)
		return
	}
	defer conn.Close()

	// Send 200 Connection Established
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// MITM TLS handshake
	tlsCfg, err := p.certCache.GetTLSConfig(host)
	if err != nil {
		log.Printf("cert generation failed for %s: %v", host, err)
		return
	}
	tlsConn := tls.Server(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("MITM TLS handshake failed for %s: %v", host, err)
		return
	}
	defer tlsConn.Close()

	p.handleTunnel(tlsConn, host, port)
}

func (p *Proxy) handleTunnel(tlsConn net.Conn, host, port string) {
	tlsConn.SetDeadline(deadlineFromNow(config.ClientRecvTimeout))
	br := bufio.NewReader(tlsConn)

	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return // Connection closed or read error
		}

		body, err := io.ReadAll(io.LimitReader(req.Body, config.MaxBodyBytes+1))
		req.Body.Close()
		if err != nil {
			sendError(tlsConn, 400, "Bad Request")
			return
		}
		if int64(len(body)) > config.MaxBodyBytes {
			sendError(tlsConn, 413, "Content Too Large")
			return
		}

		// Reset deadline for each request
		tlsConn.SetDeadline(deadlineFromNow(config.ClientRecvTimeout))

		routeModel, strippedBody := detectLocalRoute(body)
		if routeModel != "" {
			log.Printf("LOCAL_ROUTE %s https://%s:%s%s → model=%s",
				req.Method, host, port, req.URL.RequestURI(), routeModel)

			// Log headers without auth
			cleanHeaders := make(http.Header)
			for k, v := range req.Header {
				kl := strings.ToLower(k)
				if kl != "x-api-key" && kl != "authorization" {
					cleanHeaders[k] = v
				}
			}
			log.Printf("Headers: %v", cleanHeaders)

			isStreaming := false
			var data map[string]interface{}
			if json.Unmarshal(strippedBody, &data) == nil {
				if s, ok := data["stream"].(bool); ok {
					isStreaming = s
				}
			}
			sendLocalStub(tlsConn, routeModel, isStreaming)
		} else {
			if !p.forwardUpstream(tlsConn, host, port, req, body) {
				return
			}
		}

		if req.Close {
			return
		}
	}
}

var hopByHop = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"transfer-encoding": true,
	"te":                true,
	"trailers":          true,
	"upgrade":           true,
}

func (p *Proxy) forwardUpstream(tlsConn net.Conn, host, port string, req *http.Request, body []byte) bool {
	var url string
	if port == "443" {
		url = "https://" + host + req.URL.RequestURI()
	} else {
		url = "https://" + net.JoinHostPort(host, port) + req.URL.RequestURI()
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = strings.NewReader(string(body))
	}

	upReq, err := http.NewRequest(req.Method, url, bodyReader)
	if err != nil {
		sendError(tlsConn, 502, "Bad Gateway")
		return false
	}

	// Copy headers, skip hop-by-hop
	for k, vals := range req.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			upReq.Header.Add(k, v)
		}
	}
	if len(body) > 0 {
		upReq.ContentLength = int64(len(body))
	}

	resp, err := p.httpClient.Do(upReq)
	if err != nil {
		log.Printf("upstream error for %s: %v", host, err)
		sendError(tlsConn, 502, "Bad Gateway")
		return false
	}
	defer resp.Body.Close()

	// Build HTTP/1.1 response headers, stripping hop-by-hop
	hasCL := resp.ContentLength >= 0

	if hasCL {
		// Stream directly with known Content-Length
		writeResponseHeaders(tlsConn, resp)
		if _, err := io.Copy(tlsConn, resp.Body); err != nil {
			log.Printf("response streaming error for %s: %v", host, err)
			return false
		}
	} else {
		// Buffer body and add Content-Length
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxBodyBytes+1))
		if err != nil {
			log.Printf("response read error for %s: %v", host, err)
			return false
		}
		if int64(len(respBody)) > config.MaxBodyBytes {
			log.Printf("response from %s exceeded size limit", host)
			sendError(tlsConn, 502, "Bad Gateway")
			return false
		}
		writeResponseHeadersWithCL(tlsConn, resp, len(respBody))
		tlsConn.Write(respBody)
	}

	return true
}

func writeResponseHeaders(w io.Writer, resp *http.Response) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n", resp.Status) // "200 OK"
	for k, vals := range resp.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprint(w, "\r\n")
}

func writeResponseHeadersWithCL(w io.Writer, resp *http.Response, bodyLen int) {
	fmt.Fprintf(w, "HTTP/1.1 %s\r\n", resp.Status)
	for k, vals := range resp.Header {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vals {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n", bodyLen)
	fmt.Fprint(w, "\r\n")
}

func sendError(w io.Writer, code int, status string) {
	body := status
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, status, len(body), body)
}

func deadlineFromNow(d time.Duration) time.Time {
	return time.Now().Add(d)
}
