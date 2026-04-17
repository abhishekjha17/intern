# Intern — Design Document

Intern is a transparent proxy and profiler for the Anthropic Claude API. It sits between Claude clients (Claude Code, Cursor, etc.) and `api.anthropic.com`, captures every request/response as structured JSONL traces, and provides offline analysis of cost, token usage, tool calls, and conversation patterns.

## Architecture

```
Client (Claude Code / Cursor)
  │
  ▼  Anthropic Messages API
┌─────────────────────────────┐
│   httputil.ReverseProxy     │  :8080
│   + LoggingRoundTripper     │
│   (cmd/intern, internal/    │
│    logger)                  │
└─────────┬───────────────────┘
          │
          ▼
   ┌──────────────┐       ┌─────────────────┐
   │  Anthropic   │       │  traces.jsonl    │
   │  Cloud API   │       │  (async write)   │
   └──────────────┘       └─────────────────┘
                                  │
                                  ▼
                          ┌───────────────┐
                          │ intern profile │
                          │ (profiler pkg) │
                          └───────────────┘
```

## Package Layout

```
cmd/
  intern/
    main.go                       Entry point, subcommand dispatch, version
internal/
  logger/
    roundtripper.go               HTTP RoundTripper — captures request/response, writes JSONL
    roundtripper_test.go          Tests for SSE parsing, token extraction
  profiler/
    types.go                      All profiler structs (MessageProfile, ProfileReport, etc.)
    pricing.go                    Model pricing constants, CostForTokens()
    extract.go                    Response/request parsing (SSE + JSON content blocks)
    classify.go                   Phase, complexity, dependency, offload classifiers
    profiler.go                   LoadTraces(), Analyze() — core profiling pipeline
    report.go                     Text table rendering (tabwriter)
    report_json.go                JSON output
    profiler_test.go              Unit tests for all analysis functions
.claude/
  agents/                         Custom Claude Code subagents (reviewer, bug-finder, etc.)
  skills/                         Slash commands (/review, /explain, /write-tests, etc.)
Makefile                          Build, test, lint, install targets
.goreleaser.yaml                  Cross-platform release configuration
.github/workflows/
  ci.yml                          Test + lint on push/PR
  release.yml                     GoReleaser on tag push
```

## Proxy

The proxy uses `httputil.NewSingleHostReverseProxy` with a custom `http.RoundTripper` (`LoggingRoundTripper`) that:

1. Captures the full request body before forwarding
2. Tees the response body while streaming it back to the client
3. Parses SSE events to extract token usage (input, output, cache read, cache creation, thinking)
4. Derives a session ID from SHA256 of the first message in the conversation
5. Writes a `Trace` record as JSONL to disk via a buffered async writer

The proxy is fully transparent — clients see standard Anthropic API responses with no modification.

## Profiler

The profiler reads JSONL trace files and produces a `ProfileReport` with:

### Per-Message Classification
- **Phase**: exploration, execution, verification, planning, conversation — determined by tool names and bash command patterns
- **Complexity**: trivial, mechanical, reasoning, creative — score-based heuristic using output volume, tool count/diversity, thinking presence
- **Dependency**: independent, tool_continuation, conversation_continuation — based on message array structure
- **Offload candidacy**: identifies messages suitable for local models (health checks, trivial tasks, tool continuations)

### Aggregate Reports
- Cost breakdown by model (input, output, cache read, cache creation)
- Token averages by model
- Tool usage frequency
- Content block type distribution
- Session summaries (cost, duration, models, phases)
- Thinking analysis (with text vs. signature-only)
- Offload savings estimates

## CLI

```
intern [flags]                         Run proxy (default)
intern proxy --port 8080 --trace f.jsonl
intern profile [--json] <files...>     Analyze traces
intern --version
```

## Build & Test

```bash
make build        # builds ./intern with version injection
make test         # go test -race ./...
make lint         # go vet ./...
make install      # go install to $GOPATH/bin
```

## Roadmap

The following features are planned but not yet implemented:

- **Multi-model routing** — Classify requests by complexity, route simple ones to local Ollama models, pass complex ones to cloud API
- **Configurable routing rules** — User-defined routing based on tools, token count, or conversation phase
- **Web dashboard** — Browser-based visualization of traces and cost trends
- **Real-time profiling** — Live cost stats while the proxy is running
- **Budget alerts** — Spending thresholds with notifications
