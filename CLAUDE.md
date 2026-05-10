# Intern — Design Document

Intern is a transparent proxy and profiler for the Anthropic Claude API. It sits between Claude clients (Claude Code, Cursor, etc.) and `api.anthropic.com`, captures every request/response as structured JSONL traces, and provides offline analysis of cost, token usage, tool calls, and conversation patterns.

A second-generation **orchestrator** (`internal/orchestrator/`) is in development: it adds a planning step where Opus emits a `plan@v1` program that intern executes locally, pushing read-only and tool-call work onto local models and intern-internal tools. The orchestrator currently ships in shadow mode (observation only); see [Orchestrator](#orchestrator-preview) below.

## Architecture

```
Client (Claude Code / Cursor)
  │
  ▼  Anthropic Messages API
┌──────────────────────────────────────┐
│   httputil.ReverseProxy              │  :11411
│   + LoggingRoundTripper              │
│   + RequestObserver (orchestrator)   │
│   (cmd/intern, internal/logger,      │
│    internal/orchestrator)            │
└─────────┬────────────────────────────┘
          │
          ├──────────────►  Anthropic Cloud API
          │
          ▼
   ┌──────────────────┐    ┌─────────────────┐
   │  traces.jsonl    │    │  shadow.jsonl   │
   │  (every request) │    │  (orchestrator,  │
   │                  │    │   one per session)│
   └────────┬─────────┘    └─────────────────┘
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
    main.go                       Entry point, subcommand dispatch (proxy/profile/plan), version
internal/
  logger/
    roundtripper.go               HTTP RoundTripper — captures request/response, writes JSONL.
                                  Exposes RequestObserver hook the orchestrator plugs into.
    roundtripper_test.go          Tests for SSE parsing, token extraction
  profiler/                       Trace analysis (cost, tokens, phases, offload candidates)
    types.go, pricing.go, extract.go, classify.go,
    profiler.go, report.go, report_json.go, profiler_test.go
  orchestrator/                   Plan-based execution offloading
    orchestrator.go               Façade: loads ~/.intern config, bakes planner block, ExecutePlan
    shadow.go                     ShadowHook — observation-only logger of would-be planner calls
    orchestrator_test.go          Tests for façade + shadow hook
    plan/                         plan@v1 grammar — lexer, parser, AST, semantic check, predicate eval
      lexer.go (n-token lookahead), parser.go, ast.go, check.go, eval.go,
      *_test.go
    exec/                         Plan executor — DAG walker, step handlers, runtime bindings env
      exec.go, handlers.go, backends.go (LocalBackend/AgentBackend/ClientBridge interfaces),
      interp.go (string interpolation + AST→runtime), exec_test.go
    manifest/                     YAML loaders for ~/.intern/local_models.yaml and permissions.yaml
      manifest.go, permissions.go, manifest_test.go
    tools/                        Internal tool registry (Bash, Read, Grep, Glob)
      registry.go, bash.go, read.go, grep_glob.go, tools_test.go
    planner/                      Planner system-prompt template + emit_plan tool definition
      template.txt (embedded), planner.go, emit_plan.go, planner_test.go
docs/
  orchestrator/
    README.md                     Architecture and shadow-mode walkthrough
    plan-grammar.md               plan@v1 EBNF + semantic rules
    planner-system-prompt.md      The prose Opus consumes; canonical reference
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

## Orchestrator (preview)

The orchestrator turns intern from a passive observer into an active offload layer. Architecture:

1. **First request of a session** → roundtripper notices it's unfamiliar; the `RequestObserver` hook fires.
2. (Future, full mode) Intern rewrites the upstream request: prepends the planner system block (rendered from `~/.intern/local_models.yaml` + `permissions.yaml` + the client's advertised tools), adds the `emit_plan` tool, sets `tool_choice` to force the call.
3. Opus replies with an `emit_plan` tool_use carrying a `plan@v1` program.
4. Intern parses, semantically checks, and walks the plan as a DAG. Steps dispatch to:
   - `internal_tool` — invisible, runs against the registry under the permissions allowlist
   - `local` / `agent` — bounded local-model loops or fabricated Claude Code subagent calls (backends pluggable, currently stubbed)
   - `client_tool` / `ask_user` — fabricated tool_use blocks back to the IDE so the user sees and approves
   - `branch` / `parallel` / `terminal` / `escalate` — control flow
5. The terminal step's interpolated message becomes the assistant reply the client sees.

**Shadow mode** (current ship state) exercises only steps 1 and the planner-block render. No upstream rewrite, no executor invocation, no API budget burn — it just appends one JSONL entry per first-of-session request to `~/.intern/shadow.jsonl` capturing manifest hash, planner block size + sha256, and the emit_plan tool definition. This is the validation harness for the prompt-cache contract before full mode lands.

**Configuration files** (both optional; sensible defaults if absent):
- `~/.intern/local_models.yaml` — local model registry (id, capabilities, anti-capabilities, latency, etc.)
- `~/.intern/permissions.yaml` — internal tool allowlist (Bash subcommand list, etc.)

See [`docs/orchestrator/README.md`](docs/orchestrator/README.md) for the full walkthrough.

## CLI

```
intern [flags]                         Run proxy (default)
intern proxy --port 11411 --trace f.jsonl
intern proxy --orchestrate=shadow      Run proxy + shadow-mode orchestrator
intern profile [--json] <files...>     Analyze traces
intern plan                            Render the planner system prompt to stdout
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

- **Orchestrator full mode** — Step up from shadow-mode observation to the request-rewrite + plan-execute path described above. Requires a synthetic Anthropic-shaped response writer, an SSE rewriter for streaming clients, and concrete `LocalBackend` / `AgentBackend` / `ClientBridge` impls.
- **Local model backends** — Ollama and HTTP backends behind the `LocalBackend` interface so `local` steps actually invoke models.
- **Configurable routing rules** — User-defined rules for which sessions trigger orchestration (currently: every first-of-session request when shadow is on).
- **Web dashboard** — Browser-based visualization of traces and cost trends
- **Real-time profiling** — Live cost stats while the proxy is running
- **Budget alerts** — Spending thresholds with notifications
