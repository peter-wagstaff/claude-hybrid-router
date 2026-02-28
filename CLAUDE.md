# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Local MITM routing proxy for Claude Code. Sits between Claude Code (subscription) and Anthropic's API, intercepts HTTPS traffic via CONNECT + MITM TLS, detects a routing marker in the `system` field of Claude API requests, and either routes to a local/alternative model via OpenAI-compatible API or forwards unmodified to Anthropic.

**Routing marker format:** `<!-- @proxy-local-route:af83e9 model=MODEL_LABEL -->`

Only the `system` field is checked for the marker — never `messages`. This prevents contamination if an agent quotes another agent's system prompt.

## Repository Structure

```
claude-hybrid/
├── go.mod
├── cmd/claude-hybrid/main.go        # Launcher: cert gen, config load, start proxy, exec claude
├── internal/
│   ├── config/
│   │   ├── config.go                # Env-overridable constants (timeouts, limits)
│   │   └── providers.go             # YAML config parsing, model label → provider resolution
│   ├── mitm/mitm.go                 # CA generation, per-domain cert gen, LRU cache
│   ├── proxy/
│   │   ├── proxy.go                 # CONNECT handler, MITM TLS, tunnel loop, upstream/local forwarding
│   │   └── route.go                 # Route marker detection + stub response generation
│   ├── testutil/
│   │   ├── certs.go                 # Test cert generation helpers
│   │   ├── echo.go                  # Mock HTTPS echo server
│   │   └── openai.go               # Mock OpenAI chat completions server
│   └── translate/
│       ├── request.go               # Anthropic Messages → OpenAI Chat Completions request
│       ├── response.go              # OpenAI Chat Completions → Anthropic Messages response
│       └── stream.go                # OpenAI SSE → Anthropic SSE streaming state machine
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
Claude Code  --CONNECT-->  Proxy (localhost:random)
                              ├─ TLS handshake with client (MITM cert from CertCache)
                              ├─ http.ReadRequest() reads plaintext HTTP
                              ├─ Parse JSON body, check system field for routing marker
                              ├─ If marker found + config → translate to OpenAI, forward to provider, translate response back
                              ├─ If marker found, no config → return stub response
                              └─ If no marker → HTTP/2 to upstream via net/http, relay as HTTP/1.1
```

## Key Files

| File | Purpose |
|------|---------|
| `cmd/claude-hybrid/main.go` | Launcher: CA cert gen, config load, proxy start, exec claude with env vars |
| `internal/proxy/proxy.go` | Core proxy: CONNECT handler, MITM TLS, keep-alive tunnel loop, upstream forwarding, local model forwarding |
| `internal/proxy/route.go` | Route marker detection in system field + Anthropic stub response (JSON and SSE) |
| `internal/config/config.go` | Configuration: timeouts, limits (all overridable via env vars) |
| `internal/config/providers.go` | YAML config parsing (`~/.claude-hybrid/config.yaml`), model label resolution |
| `internal/mitm/mitm.go` | Dynamic per-domain cert generation + LRU tls.Certificate cache |
| `internal/translate/request.go` | Anthropic Messages API → OpenAI Chat Completions API request translation |
| `internal/translate/response.go` | OpenAI → Anthropic response translation (text, tool use, errors) |
| `internal/translate/stream.go` | OpenAI SSE → Anthropic SSE streaming state machine |

## Testing

Tests run in-process — no external services needed. Certificates are generated programmatically in memory. The proxy, echo server, and mock OpenAI server start in goroutines. Tests exercise: clean request forwarding, GET requests, keep-alive (multiple requests per tunnel), local route detection (non-streaming and streaming), marker stripping, marker-in-messages passthrough, auth header sanitization in logs, request/response translation, streaming translation, tool use round-trips, error handling.

## Development Notes

- Go 1.24+ required
- One external dependency: `gopkg.in/yaml.v3` (for config parsing)
- MITM certs generated in memory via `tls.X509KeyPair`
- CA certs stored in `~/.claude-hybrid/certs/` (auto-generated on first run)
- Provider config at `~/.claude-hybrid/config.yaml` (optional)
- Logs written to `~/.claude-hybrid/proxy.log`
- Single static binary — no runtime dependencies
