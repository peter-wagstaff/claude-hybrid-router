# claude-hybrid-router

Use Claude Code with your subscription and Anthropic's models for your main workflow, while delegating specific agents to third-party or local models like DeepSeek, Qwen, GPT-4o, or anything with an OpenAI-compatible API. Reduce costs by offloading routine tasks to cheaper or local models, while keeping Claude where it matters most.

`claude-hybrid` is a drop-in replacement for the `claude` command. It starts a transparent proxy, launches Claude Code through it, and routes requests from agents you've tagged with a routing marker to the model provider of your choice — while everything else goes to Anthropic as normal.

## Install

```bash
go install github.com/peter-wagstaff/claude-hybrid-router/cmd/claude-hybrid@latest
```

This puts `claude-hybrid` in your `$GOPATH/bin` (usually `~/go/bin`). Make sure that's in your `PATH`.

## Usage

```bash
# Just use it like claude
claude-hybrid

# Pass claude flags after --
claude-hybrid -- --dangerously-skip-permissions

# Verbose proxy logging
claude-hybrid --verbose

# Both
claude-hybrid --verbose -- --dangerously-skip-permissions

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

Logs are written to `~/.claude-hybrid/proxy.log` (auto-truncated daily). Use `--verbose` for detailed logging.

## Transforms

Providers can apply transforms to handle API quirks and extract reasoning from models that use non-standard formats. Specify transforms at the provider level (applies to all models) or per-model (overrides provider-level):

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

Available transforms:

| Transform        | Purpose                                                             |
| ---------------- | ------------------------------------------------------------------- |
| `schema:generic` | Strip `additionalProperties`, `$schema`, `strict` from tool schemas |
| `schema:openai`  | Strip `strict` only                                                 |
| `schema:gemini`  | Strip Gemini-incompatible schema fields                             |
| `reasoning`      | Convert `reasoning_content` field to Anthropic thinking blocks      |
| `extrathinktag`  | Extract `<think>` tags into thinking blocks (Qwen3, DeepSeek-R1)    |
| `forcereasoning` | Inject reasoning prompt and extract `<reasoning_content>` tags      |
| `enhancetool`    | Repair malformed tool call JSON                                     |
| `deepseek`       | Cap `max_tokens` to 8192                                            |
| `tooluse`        | Inject ExitTool for models that avoid tool use                      |
| `openrouter`     | Fix OpenRouter quirks (tool IDs, cache_control)                     |
| `groq`           | Fix Groq quirks (cache_control, `$schema`, tool IDs)                |

## Building from source

```bash
git clone https://github.com/peter-wagstaff/claude-hybrid-router.git
cd claude-hybrid-router
go build -o claude-hybrid ./cmd/claude-hybrid
```

## Testing

```bash
go test ./... -v
```

## Requirements

- Go 1.24+ to build (the binary is a static executable with zero runtime dependencies)
- `claude` CLI must be installed and in `PATH`
