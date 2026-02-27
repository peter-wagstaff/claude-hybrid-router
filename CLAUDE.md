# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Local MITM routing proxy for Claude Code. Sits between Claude Code (subscription) and Anthropic's API, intercepts HTTPS traffic via CONNECT + MITM TLS, detects a routing marker in the `system` field of Claude API requests, and either routes to a local model (stub response for now) or forwards unmodified to Anthropic.

**Routing marker format:** `<!-- @proxy-local-route:af83e9 model=MODEL_NAME -->`

Only the `system` field is checked for the marker — never `messages`. This prevents contamination if an agent quotes another agent's system prompt.

## Repository Structure

```
claude-hybrid/
├── go.mod
├── cmd/claude-hybrid/main.go    # Launcher: cert gen, start proxy, exec claude
├── internal/
│   ├── config/config.go         # Env-overridable constants
│   ├── mitm/mitm.go             # CA generation, per-domain cert gen, LRU cache
│   ├── proxy/
│   │   ├── proxy.go             # CONNECT handler, MITM TLS, tunnel loop, upstream forwarding
│   │   └── route.go             # Route marker detection + stub response generation
│   └── testutil/
│       ├── certs.go             # Test cert generation helpers
│       └── echo.go              # Mock HTTPS echo server
```

## Commands

```bash
# Run all tests
cd claude-hybrid && go test ./... -v

# Build the binary
cd claude-hybrid && go build -o claude-hybrid ./cmd/claude-hybrid

# Run it (starts proxy + claude)
./claude-hybrid

# With custom port
./claude-hybrid --port 9090

# Proxy-only mode (for testing without claude)
./claude-hybrid --proxy-only
```

## Architecture

```
Claude Code  --CONNECT-->  Proxy (localhost:8080)
                              ├─ TLS handshake with client (MITM cert from CertCache)
                              ├─ http.ReadRequest() reads plaintext HTTP
                              ├─ Parse JSON body, check system field for routing marker
                              ├─ If marker found → log, return stub response
                              └─ If no marker → HTTP/2 to upstream via net/http, relay as HTTP/1.1
```

## Key Files

| File | Purpose |
|------|---------|
| `cmd/claude-hybrid/main.go` | Launcher: CA cert gen, proxy start, exec claude with env vars |
| `internal/proxy/proxy.go` | Core proxy: CONNECT handler, MITM TLS, keep-alive tunnel loop, upstream forwarding |
| `internal/proxy/route.go` | Route marker detection in system field + Anthropic stub response (JSON and SSE) |
| `internal/config/config.go` | Configuration: timeouts, limits (all overridable via env vars) |
| `internal/mitm/mitm.go` | Dynamic per-domain cert generation + LRU tls.Certificate cache |
| `internal/testutil/echo.go` | HTTPS echo server for testing |
| `internal/testutil/certs.go` | Test cert generation helpers |

## Testing

Tests run in-process — no external services needed. Certificates are generated programmatically in memory. The proxy and echo server start in goroutines. Tests exercise: clean request forwarding, GET requests, keep-alive (multiple requests per tunnel), local route detection (non-streaming and streaming), marker stripping, marker-in-messages passthrough, auth header sanitization in logs.

## Development Notes

- Go 1.24+ required
- Zero external dependencies (stdlib only)
- MITM certs generated in memory via `tls.X509KeyPair`
- CA certs stored in `~/.claude-hybrid/certs/` (auto-generated on first run)
- Single static binary — no runtime dependencies
