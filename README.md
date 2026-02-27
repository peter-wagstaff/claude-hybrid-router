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

## How it works

```
claude-hybrid
  ├─ Generate CA cert (if first run)
  ├─ Start MITM proxy on random port
  ├─ Launch claude with HTTPS_PROXY + NODE_EXTRA_CA_CERTS
  └─ Exit when claude exits
```

The proxy intercepts HTTPS CONNECT tunnels, performs MITM TLS, and inspects the `system` field of Claude API requests for a routing marker:

```
<!-- @proxy-local-route:af83e9 model=MODEL_NAME -->
```

- **Marker found** → returns a stub response (local model routing — provider integration coming)
- **No marker** → forwards unmodified to Anthropic via HTTP/2

Only the `system` field is checked — never `messages` — preventing contamination from quoted system prompts.

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
