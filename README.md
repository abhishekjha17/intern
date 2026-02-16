# intern

the corporation of agents

An LLM routing proxy that makes your local Ollama models work as a drop-in replacement for the Anthropic API. Point Claude Code, Cursor, or any Anthropic API client at `localhost:11411` and intern will automatically route simple requests to your local model and complex ones to the cloud.

## How it works

```
Your IDE / Claude Code
        │
        ▼
   intern proxy (:11411)
    ┌────┴────┐
    │ classify │──→ "Is this simple or complex?"
    └────┬────┘
    LOCAL │    │ CLOUD
         ▼    ▼
      Ollama  Anthropic API
```

Every incoming request gets classified by a small local model. Simple tasks (basic code, math, formatting, Q&A) run locally on Ollama. Complex tasks (deep reasoning, architecture design, expert analysis) pass through to Anthropic's API.

**Full tool calling support** — tool definitions, tool_use responses, tool_result follow-ups, and multi-turn tool conversations all work locally. Streaming too.

## Quick start

**Prerequisites:**
- Go 1.21+
- [Ollama](https://ollama.ai) running locally
- A model pulled: `ollama pull qwen2.5:3b`

```bash
# Build and run
go build -o intern .
./intern

# Or just
go run .
```

The proxy starts on port `11411`. Point your client at it:

```bash
# Claude Code
export ANTHROPIC_BASE_URL=http://localhost:11411

# Or use curl directly
curl http://localhost:11411/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "hello world in python"}],
    "max_tokens": 100
  }'
```

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `11411` | Port for the proxy |
| `-ollama` | `http://localhost:11434` | Ollama server URL |
| `-cloud` | `https://api.anthropic.com` | Anthropic API URL (for cloud fallback) |
| `-model` | `qwen2.5:3b` | Ollama model for answering requests |
| `-classifier-model` | `qwen2.5:3b` | Ollama model for routing decisions |

```bash
# Use a bigger model for inference, keep classifier small and fast
./intern -model llama3.1:8b -classifier-model qwen2.5:3b
```

## What gets routed where

| Request type | Route | Example |
|-------------|-------|---------|
| Simple code | LOCAL | "write hello world in python" |
| Math / Q&A | LOCAL | "what is 2+2?" |
| Tool calls | LOCAL | function calling with tool definitions |
| Multi-turn tools | LOCAL | tool_result follow-up messages |
| Deep reasoning | CLOUD | "design a consensus algorithm with proofs" |
| Architecture | CLOUD | "design a microservices system for..." |
| Expert analysis | CLOUD | complex multi-step reasoning tasks |

The classifier defaults to CLOUD when unsure, so nothing breaks if Ollama is down.

## Features

- **Zero dependencies** — pure Go standard library, no frameworks
- **Full Anthropic Messages API compatibility** — text, streaming, system prompts, tools
- **Tool calling** — tool definitions, tool_use responses, tool_result messages, multi-turn
- **Streaming** — SSE translation between OpenAI and Anthropic formats
- **Transparent** — clients don't know they're talking to a proxy
- **Cloud fallback** — complex requests pass through to Anthropic untouched
- **Fail-safe** — any error in classification defaults to cloud routing

## Testing

Start the proxy, then run the test suite:

```bash
go run . &
./test.sh
```

7 smoke tests covering:
1. Local text routing (non-streaming)
2. Local text routing (streaming)
3. Cloud routing for complex requests
4. Local tool calling (non-streaming)
5. System prompt passthrough
6. Multi-turn tool conversations
7. Streaming tool calling

## Architecture

```
main.go                          → entry point, flags, wiring
internal/
  classifier/classifier.go      → LLM-based LOCAL/CLOUD routing
  router/bounce.go               → route decision wrapper
  proxy/handler.go               → HTTP handler, local/cloud dispatch
  models/request.go              → all Anthropic + Ollama type definitions
  translator/
    request.go                   → Anthropic → Ollama request translation
    response.go                  → Ollama → Anthropic response translation
    stream.go                    → Ollama SSE → Anthropic SSE streaming
test.sh                          → smoke tests
```

## How the format translation works

The proxy translates between Anthropic's Messages API format and Ollama's OpenAI-compatible chat completions API. The main differences:

**Tool definitions:**
- Anthropic: `{name, description, input_schema}`
- Ollama: `{type: "function", function: {name, description, parameters}}`

**Tool calls in responses:**
- Anthropic: content blocks with `type: "tool_use"`
- Ollama: `message.tool_calls[]` array

**Tool results in history:**
- Anthropic: user message content block with `type: "tool_result"`
- Ollama: separate message with `role: "tool"`

**Streaming:**
- Anthropic: lifecycle events (`message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, `message_stop`)
- Ollama: `data: {json}` lines with delta objects
