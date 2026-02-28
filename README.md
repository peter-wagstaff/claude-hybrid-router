# claude-hybrid

Drop-in replacement command for `claude` that routes marked requests to a local model. Run `claude-hybrid` instead of `claude` — it starts a transparent MITM proxy, launches Claude Code through it, and intercepts requests tagged with a routing marker.

## Install

```bash
go install github.com/peter-wagstaff/claude-hybrid-router/cmd/claude-hybrid@latest
```

This puts `claude-hybrid` in your `$GOPATH/bin` (usually `~/go/bin`). Make sure that's in your `PATH`.

## Usage

```bash
# Just use it like claude
claude-hybrid

# Pass any claude flags through
claude-hybrid --model sonnet

# With custom proxy port
claude-hybrid --port 9090
```

On first run, it auto-generates a MITM CA certificate at `~/.claude-hybrid/certs/`. No manual setup needed.

## Routing to local/alternative models

Create `~/.claude-hybrid/config.yaml` to route requests to any OpenAI-compatible API:

```yaml
providers:
  - name: ollama
    endpoint: http://localhost:11434/v1
    models:
      fast_coder: qwen3:32b
      reasoning: deepseek-r1:14b

  - name: openai
    endpoint: https://api.openai.com/v1
    api_key: ${OPENAI_API_KEY}
    max_tokens: 16384
    models:
      gpt4mini: gpt-4o-mini
```

- **Model labels** (left side) are referenced in the routing marker
- **Model names** (right side) are sent to the provider's API
- `api_key` supports `${VAR}` env var expansion, or you can put the key directly
- `max_tokens` caps the token limit per provider (some models have lower limits than Claude)

Then add the routing marker to a Claude Code agent's system prompt (e.g., `.claude/agents/my-agent.md`):

```
<!-- @proxy-local-route:af83e9 model=fast_coder -->
```

When Claude Code dispatches that agent, the proxy intercepts the request, translates it from Anthropic's API format to OpenAI's, sends it to the configured provider, and translates the response back.

Without a config file, routed requests return a stub response.

## How it works

```
claude-hybrid
  ├─ Generate CA cert (if first run)
  ├─ Start MITM proxy on random port
  ├─ Load ~/.claude-hybrid/config.yaml (if exists)
  ├─ Launch claude with HTTPS_PROXY + NODE_EXTRA_CA_CERTS
  └─ Exit when claude exits
```

The proxy intercepts HTTPS CONNECT tunnels, performs MITM TLS, and inspects the `system` field of Claude API requests for a routing marker. Only the `system` field is checked — never `messages` — preventing contamination from quoted system prompts.

- **Marker found + config** → translates request to OpenAI format, forwards to provider, translates response back
- **Marker found, no config** → returns stub response
- **No marker** → forwards unmodified to Anthropic via HTTP/2

Logs are written to `~/.claude-hybrid/proxy.log`.

## Configuration

Environment variables (all optional):

| Variable | Default | Purpose |
|----------|---------|---------|
| `UPSTREAM_TIMEOUT_SECS` | 30 | Upstream connection timeout |
| `MAX_BODY_BYTES` | 10485760 | Max request/response body (10 MB) |
| `MAX_PROXY_GOROUTINES` | 128 | Concurrent connection limit |
| `CLIENT_RECV_TIMEOUT_SECS` | 300 | Client receive timeout (5 min) |
| `MITM_CACHE_MAX_SIZE` | 256 | LRU cert cache size |

## Building from source

```bash
git clone https://github.com/peter-wagstaff/claude-hybrid-router/git
cd claude-hybrid/claude-hybrid
go build -o claude-hybrid ./cmd/claude-hybrid
```

## Testing

```bash
cd claude-hybrid && go test ./... -v
```

## Requirements

- Go 1.24+ to build (the binary is a static executable with zero runtime dependencies)
- `claude` CLI must be installed and in `PATH`
