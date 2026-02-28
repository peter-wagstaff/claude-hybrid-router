// Package proxy implements the MITM CONNECT proxy with local model routing.
package proxy

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/peter-wagstaff/claude-hybrid-router/internal/config"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/mitm"
	"github.com/peter-wagstaff/claude-hybrid-router/internal/translate"
)

// Proxy is an HTTP handler that handles CONNECT requests with MITM TLS.
type Proxy struct {
	certCache     *mitm.CertCache
	httpClient    *http.Client
	localClient   *http.Client
	modelResolver *config.ModelResolver
	sem           chan struct{}
	verbose       bool
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithVerbose enables verbose logging.
func WithVerbose(v bool) Option {
	return func(p *Proxy) { p.verbose = v }
}

// WithHTTPClient sets a custom HTTP client for upstream requests.
func WithHTTPClient(c *http.Client) Option {
	return func(p *Proxy) { p.httpClient = c }
}

// WithModelResolver sets the model resolver for local routing.
func WithModelResolver(r *config.ModelResolver) Option {
	return func(p *Proxy) { p.modelResolver = r }
}

// WithLocalClient sets a custom HTTP client for local model requests.
func WithLocalClient(c *http.Client) Option {
	return func(p *Proxy) { p.localClient = c }
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
	if p.localClient == nil {
		p.localClient = &http.Client{
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
		p.logVerbose("hijack error: %v", err)
		return
	}
	defer conn.Close()

	// Send 200 Connection Established
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// MITM TLS handshake
	tlsCfg, err := p.certCache.GetTLSConfig(host)
	if err != nil {
		p.logVerbose("cert generation failed for %s: %v", host, err)
		return
	}
	tlsConn := tls.Server(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		p.logVerbose("MITM TLS handshake failed for %s: %v", host, err)
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
			streamMode := "non-streaming"
			var reqMeta struct{ Stream bool `json:"stream"` }
			if json.Unmarshal(body, &reqMeta) == nil && reqMeta.Stream {
				streamMode = "streaming"
			}
			log.Printf("LOCAL_ROUTE %s https://%s:%s%s → model=%s (%s)",
				req.Method, host, port, req.URL.RequestURI(), routeModel, streamMode)

			p.forwardLocal(tlsConn, routeModel, strippedBody)
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
		if p.verbose || isAPIHost(host) {
			log.Printf("upstream error for %s: %v", host, err)
		}
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
			p.logVerbose("response streaming error for %s: %v", host, err)
			return false
		}
	} else {
		// Buffer body and add Content-Length
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxBodyBytes+1))
		if err != nil {
			p.logVerbose("response read error for %s: %v", host, err)
			return false
		}
		if int64(len(respBody)) > config.MaxBodyBytes {
			p.logVerbose("response from %s exceeded size limit", host)
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

func (p *Proxy) forwardLocal(w io.Writer, modelLabel string, body []byte) {
	if p.modelResolver == nil {
		// No config — fall back to stub response
		isStreaming := false
		var data map[string]interface{}
		if json.Unmarshal(body, &data) == nil {
			if s, ok := data["stream"].(bool); ok {
				isStreaming = s
			}
		}
		sendLocalStub(w, modelLabel, isStreaming)
		return
	}

	start := time.Now()

	resolved, err := p.modelResolver.Resolve(modelLabel)
	if err != nil {
		log.Printf("model resolution failed: %v", err)
		errBody := translate.FormatError("invalid_request_error",
			fmt.Sprintf("Unknown model label %q — check ~/.claude-hybrid/config.yaml", modelLabel))
		sendAnthropicError(w, 400, errBody)
		return
	}

	// Build transform chain
	chain, err := translate.BuildChain(resolved.Transform)
	if err != nil {
		log.Printf("transform chain build failed for %v: %v — falling back to no transforms", resolved.Transform, err)
		chain = translate.NewTransformChain()
	}
	ctx := translate.NewTransformContext(resolved.Model, resolved.Provider)

	// Translate request body
	oaiBody, err := translate.RequestToOpenAI(body, resolved.Model, resolved.MaxTokens)
	if err != nil {
		log.Printf("request translation failed: %v", err)
		errBody := translate.FormatError("api_error", fmt.Sprintf("Request translation failed: %v", err))
		sendAnthropicError(w, 500, errBody)
		return
	}

	// Run request transforms
	var oaiReq map[string]interface{}
	if err := json.Unmarshal(oaiBody, &oaiReq); err == nil {
		if err := chain.RunRequest(oaiReq, ctx); err != nil {
			log.Printf("[LOCAL_ERR:TRANSLATE] request transform failed for %s: %v", modelLabel, err)
			errBody := translate.FormatError("api_error",
				fmt.Sprintf("[TRANSLATE] Request transform failed for '%s': %v", modelLabel, err))
			sendAnthropicError(w, 500, errBody)
			return
		}
		oaiBody, _ = json.Marshal(oaiReq)
	}

	// Determine if streaming
	isStreaming := false
	var data map[string]interface{}
	if json.Unmarshal(body, &data) == nil {
		if s, ok := data["stream"].(bool); ok {
			isStreaming = s
		}
	}

	// Build request to local provider
	endpoint := resolved.Endpoint + "/chat/completions"
	localReq, err := http.NewRequest("POST", endpoint, strings.NewReader(string(oaiBody)))
	if err != nil {
		log.Printf("failed to create local request: %v", err)
		errBody := translate.FormatError("api_error", fmt.Sprintf("Failed to create request: %v", err))
		sendAnthropicError(w, 500, errBody)
		return
	}
	localReq.Header.Set("Content-Type", "application/json")
	if resolved.APIKey != "" {
		localReq.Header.Set("Authorization", "Bearer "+resolved.APIKey)
	}

	resp, err := p.localClient.Do(localReq)
	if err != nil {
		cat := translate.ClassifyError(err)
		log.Printf("[LOCAL_ERR:%s] %s unreachable: %v (%s)", cat, modelLabel, err, endpoint)
		errBody := translate.FormatError("api_error",
			fmt.Sprintf("[%s] Local model '%s' unreachable: %v (%s)", cat, modelLabel, err, endpoint))
		sendAnthropicError(w, 502, errBody)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		sanitized := sanitizeForLog(string(respBody))
		log.Printf("[LOCAL_ERR:HTTP_%d] %s returned %d: %s", resp.StatusCode, modelLabel, resp.StatusCode, sanitized)
		errBody := translate.FormatError("api_error",
			fmt.Sprintf("[HTTP_%d] Local provider '%s' returned %d: %s", resp.StatusCode, modelLabel, resp.StatusCode, sanitized))
		sendAnthropicError(w, 502, errBody)
		return
	}

	if isStreaming {
		// Stream: translate OpenAI SSE → Anthropic SSE
		var sseBuf bytes.Buffer
		st := translate.NewStreamTranslator(modelLabel)
		st.SetVerbose(p.verbose)
		st.SetTransformChain(chain, ctx)
		streamErr := st.TranslateStream(resp.Body, &sseBuf)
		sseBody := sseBuf.Bytes()
		if streamErr != nil {
			cat := translate.ClassifyError(streamErr)
			log.Printf("[LOCAL_ERR:%s] stream translation error for %s: %v", cat, modelLabel, streamErr)
			if len(sseBody) == 0 {
				errBody := translate.FormatError("api_error",
					fmt.Sprintf("[%s] Stream translation failed for '%s': %v", cat, modelLabel, streamErr))
				sendAnthropicError(w, 502, errBody)
				return
			}
			sseBody = append(sseBody, translate.FormatStreamError("api_error",
				fmt.Sprintf("[%s] Stream interrupted for '%s': %v", cat, modelLabel, streamErr))...)
		}
		fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(sseBody))
		w.Write(sseBody)
		if streamErr == nil {
			log.Printf("LOCAL_OK %s → %s/%s (streaming, %dms)",
				modelLabel, resolved.Provider, resolved.Model, time.Since(start).Milliseconds())
		}
	} else {
		// Non-streaming: translate response
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxBodyBytes+1))
		if err != nil {
			cat := translate.ClassifyError(err)
			log.Printf("[LOCAL_ERR:%s] response read error for %s: %v", cat, modelLabel, err)
			errBody := translate.FormatError("api_error",
				fmt.Sprintf("[%s] Failed to read response from '%s': %v", cat, modelLabel, err))
			sendAnthropicError(w, 502, errBody)
			return
		}
		respBody, _ = chain.RunResponse(respBody, ctx)
		aBody, err := translate.ResponseToAnthropic(respBody, modelLabel)
		if err != nil {
			log.Printf("[LOCAL_ERR:TRANSLATE] response translation failed for %s: %v", modelLabel, err)
			errBody := translate.FormatError("api_error",
				fmt.Sprintf("[TRANSLATE] Response translation failed for '%s': %v", modelLabel, err))
			sendAnthropicError(w, 502, errBody)
			return
		}
		fmt.Fprintf(w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(aBody))
		w.Write(aBody)
		// Extract token usage from translated response
		var aResp struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		json.Unmarshal(aBody, &aResp)
		log.Printf("LOCAL_OK %s → %s/%s (%dms, in=%d out=%d tokens)",
			modelLabel, resolved.Provider, resolved.Model, time.Since(start).Milliseconds(),
			aResp.Usage.InputTokens, aResp.Usage.OutputTokens)
	}
}

func sendAnthropicError(w io.Writer, httpStatus int, body []byte) {
	fmt.Fprintf(w, "HTTP/1.1 %d Error\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		httpStatus, len(body))
	w.Write(body)
}

func sendError(w io.Writer, code int, status string) {
	body := status
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, status, len(body), body)
}

func deadlineFromNow(d time.Duration) time.Time {
	return time.Now().Add(d)
}

func (p *Proxy) logVerbose(format string, args ...interface{}) {
	if p.verbose {
		log.Printf(format, args...)
	}
}

// isAPIHost returns true for hosts where upstream errors are worth logging.
func isAPIHost(host string) bool {
	return strings.Contains(host, "anthropic.com") ||
		strings.Contains(host, "openai.com") ||
		strings.Contains(host, "localhost") ||
		strings.Contains(host, "127.0.0.1")
}

var bearerRE = regexp.MustCompile(`(?i)bearer\s+\S+`)
var apiKeyRE = regexp.MustCompile(`(?i)(sk-|key-)[a-zA-Z0-9]{8,}`)

// sanitizeForLog redacts Bearer tokens and API key patterns from text.
func sanitizeForLog(s string) string {
	s = bearerRE.ReplaceAllString(s, "Bearer [REDACTED]")
	s = apiKeyRE.ReplaceAllString(s, "$1[REDACTED]")
	return s
}
