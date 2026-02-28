# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Local MITM routing proxy for Claude Code. Sits between Claude Code (subscription) and Anthropic's API, intercepts HTTPS traffic via CONNECT + MITM TLS, detects a routing marker in the `system` field of Claude API requests, and either routes to a local/alternative model via OpenAI-compatible API or forwards unmodified to Anthropic.

**Routing marker format:** `<!-- @proxy-local-route:af83e9 model=MODEL_LABEL -->`

Only the `system` field is checked for the marker — never `messages`. This prevents contamination if an agent quotes another agent's system prompt.

## Repository Structure

```
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
│       ├── transformer.go           # Transformer interface, TransformChain, TransformContext
│       ├── transform_registry.go    # Transform name → constructor registry, BuildChain
│       ├── transform.go             # Schema cleaning (SchemaTransformer, fieldStripper, geminiTransformer)
│       ├── transform_reasoning.go   # reasoning_content → thinking blocks
│       ├── transform_enhancetool.go # Repair malformed tool call JSON
│       ├── transform_deepseek.go    # max_tokens cap
│       ├── transform_thinktag.go    # <think> tag extraction FSM
│       ├── transform_openrouter.go  # OpenRouter quirks (tool IDs, cache_control, reasoning field)
│       ├── transform_groq.go        # Groq quirks (cache_control, $schema, tool IDs)
│       ├── transform_tooluse.go     # ExitTool injection/interception
│       ├── transform_forcereasoning.go # Inject reasoning prompt, extract tags
│       ├── jsonfix.go               # Relaxed JSON parser for tool argument repair
│       ├── request.go               # Anthropic → OpenAI request translation
│       ├── response.go              # OpenAI → Anthropic response translation
│       └── stream.go                # OpenAI SSE → Anthropic SSE streaming
```

## Commands

```bash
# Run all tests
go test ./... -v

# Build the binary
go build -o claude-hybrid ./cmd/claude-hybrid

# Run it (starts proxy + claude)
./claude-hybrid

# Pass claude flags after --
./claude-hybrid -- --dangerously-skip-permissions

# Verbose logging
./claude-hybrid --verbose

# Proxy-only mode (for testing without claude)
./claude-hybrid --proxy-only

# Integration test against real provider (requires proxy running)
go run ./cmd/integration-test -proxy HOST:PORT [-stream]
```

## Architecture

```
Claude Code  --CONNECT-->  Proxy (localhost:random)
                              ├─ TLS handshake with client (MITM cert from CertCache)
                              ├─ http.ReadRequest() reads plaintext HTTP
                              ├─ Parse JSON body, check system field for routing marker
                              ├─ If marker found + config:
                              │   ├─ Translate Anthropic → OpenAI (RequestToOpenAI)
                              │   ├─ Build transform chain from provider config
                              │   ├─ Run request transforms (schema cleaning, tool injection, etc.)
                              │   ├─ Forward to local provider
                              │   ├─ Run response/stream transforms (reasoning, tool repair, etc.)
                              │   └─ Translate OpenAI → Anthropic (ResponseToAnthropic / StreamTranslator)
                              ├─ If marker found, no config → return stub response
                              └─ If no marker → HTTP/2 to upstream via net/http, relay as HTTP/1.1
```

## Key Files

| File | Purpose |
|------|---------|
| `cmd/claude-hybrid/main.go` | Launcher: CA cert gen (with lock file for multi-instance safety), config load, proxy start, graceful shutdown, exec claude with env vars |
| `internal/proxy/proxy.go` | Core proxy: CONNECT handler, MITM TLS, keep-alive tunnel loop, upstream forwarding, local model forwarding |
| `internal/proxy/route.go` | Route marker detection in system field + Anthropic stub response (JSON and SSE) |
| `internal/config/config.go` | Constants: timeouts, body size limits, concurrency cap |
| `internal/config/providers.go` | YAML config parsing (`~/.claude-hybrid/config.yaml`), model label resolution |
| `internal/mitm/mitm.go` | Dynamic per-domain cert generation + LRU tls.Certificate cache |
| `internal/translate/transformer.go` | Transformer interface, TransformChain, TransformContext |
| `internal/translate/transform_registry.go` | Transform name → constructor registry, BuildChain |
| `internal/translate/transform.go` | Schema cleaning transforms (generic, openai, gemini, ollama) |
| `internal/translate/transform_reasoning.go` | Converts reasoning_content → Anthropic thinking blocks |
| `internal/translate/transform_enhancetool.go` | Repairs malformed tool call JSON arguments |
| `internal/translate/transform_deepseek.go` | Caps max_tokens to 8192 for DeepSeek |
| `internal/translate/transform_thinktag.go` | Extracts `<think>` tags from content into thinking blocks |
| `internal/translate/transform_openrouter.go` | Fixes OpenRouter quirks (tool IDs, cache_control, reasoning) |
| `internal/translate/transform_groq.go` | Fixes Groq quirks (cache_control, $schema, tool IDs) |
| `internal/translate/transform_tooluse.go` | ExitTool injection for models that avoid tool use |
| `internal/translate/transform_forcereasoning.go` | Injects reasoning prompt and extracts reasoning tags |
| `internal/translate/jsonfix.go` | Relaxed JSON parser for tool argument repair |
| `internal/translate/request.go` | Anthropic Messages API → OpenAI Chat Completions API request translation |
| `internal/translate/response.go` | OpenAI → Anthropic response translation, error classification (ClassifyError), SSE error formatting (FormatStreamError) |
| `internal/translate/stream.go` | OpenAI SSE → Anthropic SSE streaming state machine, consecutive-drop abort |

## Provider Config with Transforms

Providers can specify a `transform` array at the provider level (applied to all models) and/or per-model:

```yaml
providers:
  - name: deepseek
    endpoint: https://api.deepseek.com/v1
    api_key: ${DEEPSEEK_API_KEY}
    transform: ["deepseek", "reasoning", "enhancetool", "schema:generic"]
    models:
      reasoner: deepseek-reasoner
      chat:
        model: deepseek-chat
        transform: ["tooluse", "enhancetool", "schema:generic"]
```

Per-model `transform` overrides the provider-level `transform` (no merging).

## Available Transforms

| Transform | What it does |
|-----------|-------------|
| `schema:generic` | Strips additionalProperties, $schema, strict from tool schemas |
| `schema:openai` | Strips only strict |
| `schema:gemini` | Strips Gemini-incompatible schema fields and format values |
| `schema:ollama` | Same as generic |
| `reasoning` | Converts reasoning_content → Anthropic thinking blocks |
| `enhancetool` | Repairs malformed tool call JSON arguments |
| `deepseek` | Caps max_tokens to 8192 |
| `extrathinktag` | Extracts `<think>` tags from content into thinking blocks |
| `openrouter` | Fixes OpenRouter quirks (tool IDs, cache_control, reasoning field) |
| `groq` | Fixes Groq quirks (cache_control, $schema, tool IDs) |
| `tooluse` | Injects ExitTool for models that avoid tool use |
| `forcereasoning` | Injects reasoning prompt and extracts reasoning tags |

## Testing

Tests run in-process — no external services needed. Certificates are generated programmatically in memory. The proxy, echo server, and mock OpenAI server start in goroutines. Tests exercise: clean request forwarding, GET requests, keep-alive (multiple requests per tunnel), local route detection (non-streaming and streaming), marker stripping, marker-in-messages passthrough, auth header sanitization in logs, request/response translation, streaming translation, tool use round-trips, error handling, and local provider error propagation (truncated responses, garbled SSE streams).

## Development Notes

- Go 1.24+ required
- One external dependency: `gopkg.in/yaml.v3` (for config parsing)
- MITM certs generated in memory via `tls.X509KeyPair`
- CA certs stored in `~/.claude-hybrid/certs/` (auto-generated on first run, lock file prevents races)
- Provider config at `~/.claude-hybrid/config.yaml` (optional)
- Logs written to `~/.claude-hybrid/proxy.log` (daily rotation with flock, session ID prefix `[s<pid>]`)
- `--verbose` enables detailed logging (including dropped SSE chunks); default is sparse (LOCAL_ROUTE + LOCAL_OK + LOCAL_ERR)
- Error log prefixes: `[LOCAL_ERR:CONNECTION]`, `[LOCAL_ERR:TIMEOUT]`, `[LOCAL_ERR:HTTP_N]`, `[LOCAL_ERR:TRANSLATE]`, `[LOCAL_ERR:PARSE]`
- API keys in provider error responses are redacted before logging
- Multiple instances safe: each gets its own proxy port, shares CA cert (read-only) and log file (append)
- Graceful shutdown: 5s timeout for in-flight requests when Claude exits
- Single static binary — no runtime dependencies
