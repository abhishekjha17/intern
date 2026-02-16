# Intern — Agent Design Document

Intern is an LLM routing proxy that sits between Anthropic API clients (Claude Code, Cursor, etc.) and LLM backends. It accepts Anthropic Messages API requests, classifies them by complexity, and routes simple requests to a local Ollama model while passing complex requests through to the Anthropic cloud API. The proxy is fully transparent — clients don't know they're talking to a proxy.

## Architecture

```
Client (Claude Code / Cursor)
  │
  ▼  Anthropic Messages API format
┌─────────────────────────┐
│     ProxyHandler        │  :11411
│  (internal/proxy)       │
├─────────────────────────┤
│       Router            │
│  (internal/router)      │
├─────────────────────────┤
│     Classifier          │  Calls Ollama with zero-shot prompt
│  (internal/classifier)  │  Returns LOCAL or CLOUD
└────────┬────────┬───────┘
         │        │
    LOCAL │        │ CLOUD
         ▼        ▼
   ┌──────────┐  ┌──────────────┐
   │  Ollama  │  │  Anthropic   │
   │ :11434   │  │  Cloud API   │
   └──────────┘  └──────────────┘
```

## Request Flow

1. `ProxyHandler.ServeHTTP` receives request, unmarshals into `AnthropicRequest`
2. `Router.Decide` calls `Classifier.Classify` which:
   - Short-circuits to LOCAL if `tool_result` blocks are present (continuing a tool conversation)
   - Extracts text from the last user message via `Message.ExtractText()`
   - Sends it to a small Ollama model with a zero-shot classification prompt
   - Returns `"LOCAL"` or `"CLOUD"` (defaults to CLOUD on any error)
3. **LOCAL path**: `translator.AnthropicToOllama()` converts the request, POSTs to Ollama's `/v1/chat/completions`, then `translator.OllamaToAnthropic()` or `translator.StreamOllamaToAnthropic()` converts the response back
4. **CLOUD path**: reverse proxy passthrough to `api.anthropic.com`, untouched

## Package Layout

```
main.go                           Entry point, flag parsing, wiring
internal/
  models/request.go               All type definitions (Anthropic + Ollama)
  classifier/classifier.go        LLM-based LOCAL/CLOUD classifier
  router/bounce.go                Router (wraps classifier into RouteDecision)
  proxy/handler.go                HTTP handler, local/cloud dispatch
  translator/
    request.go                    Anthropic request → Ollama request
    response.go                   Ollama response → Anthropic response
    stream.go                     Ollama SSE stream → Anthropic SSE stream
test.sh                           Smoke tests (7 tests)
```

## Key Types

**`models.Message`** — Anthropic message with `Content json.RawMessage`. Content is either a plain JSON string `"hello"` or a content block array `[{"type":"text","text":"..."}, {"type":"tool_use",...}]`. Use `ExtractText()` for plain text or `ParseContentBlocks()` for the array form.

**`models.ContentBlock`** — Union struct covering `text`, `tool_use`, and `tool_result` block types. Fields are tagged `omitempty` so only relevant ones serialize.

**`models.AnthropicRequest`** — `System` and `Tools` are both `json.RawMessage` (can be string or array). Use `ExtractSystemText()`, `HasTools()`, `ParseTools()`.

## Format Translation

The translator handles these mappings between Anthropic and OpenAI/Ollama formats:

| Concept | Anthropic | OpenAI/Ollama |
|---------|-----------|---------------|
| Tool definition | `{name, description, input_schema}` | `{type:"function", function:{name, description, parameters}}` |
| Tool invocation | Content block: `{type:"tool_use", id, name, input}` | `message.tool_calls[]: {id, type:"function", function:{name, arguments}}` |
| Tool result | User message content block: `{type:"tool_result", tool_use_id, content}` | Separate message: `{role:"tool", tool_call_id, content}` |
| Stop reason | `"end_turn"`, `"tool_use"`, `"max_tokens"` | `"stop"`, `"tool_calls"`, `"length"` |
| Streaming | `event: content_block_start/delta/stop` per block | Single `data:` lines with `delta` object |

## Streaming Translation

Ollama sends tool calls all at once in one delta chunk (not incrementally). The stream translator:
1. Emits `message_start` immediately
2. Lazily emits `content_block_start` for text only when text arrives
3. On `delta.tool_calls`: closes any open text block, then for each tool call emits `content_block_start` (type:tool_use) → `content_block_delta` (input_json_delta) → `content_block_stop`
4. Emits `message_delta` with `stop_reason` and `message_stop`

## Classifier Behavior

- Uses zero-shot prompt with `temperature: 0`, `max_tokens: 3`
- 5-second timeout, falls back to CLOUD on any error
- Fuzzy matches response for "LOCAL" or "CLOUD" keywords
- **Tool result short-circuit**: if any message contains `tool_result` blocks, routes LOCAL immediately (assumes ongoing local tool conversation)

## Build & Test

```bash
go build ./...           # zero external dependencies
go run .                 # starts on :11411
./test.sh                # 7 smoke tests (requires running proxy + Ollama)
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `11411` | Proxy listen port |
| `-ollama` | `http://localhost:11434` | Ollama URL |
| `-cloud` | `https://api.anthropic.com` | Anthropic API URL |
| `-model` | `qwen2.5:3b` | Ollama model for inference |
| `-classifier-model` | `qwen2.5:3b` | Ollama model for routing classification |
