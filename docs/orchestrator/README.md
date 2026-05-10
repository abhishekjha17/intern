# Orchestrator

The orchestrator turns intern from a passive tracing proxy into an active offload layer. The idea: when a session starts, ask Opus to emit a *plan* — a small DAG program in the `plan@v1` grammar — that intern then executes locally, dispatching read-only exploration to internal tools and bounded loops to local models, and only surfacing state-mutating actions to the user's IDE for approval.

This document is the architecture and walkthrough. Details live in:

- [`plan-grammar.md`](plan-grammar.md) — full EBNF + semantic rules for `plan@v1`
- [`planner-system-prompt.md`](planner-system-prompt.md) — the prose Opus consumes (with placeholders); canonical reference

## Data flow

```
                  Client (Claude Code, Cursor, …)
                              │
                              ▼  POST /v1/messages
       ┌───────────────────────────────────────────────────────┐
       │  intern proxy (LoggingRoundTripper)                    │
       │  ── snapshots request body                              │
       │  ── derives session_id from sha256(messages[0])         │
       │  ── fires RequestObserver hook ─┐                       │
       │  ── forwards upstream            │                      │
       └─────────────────────┬────────────┼──────────────────────┘
                             │            │
                             ▼            ▼
                     api.anthropic.com   ShadowHook
                                          │
                                          ▼
                                   ~/.intern/shadow.jsonl
                                   (one entry per first-of-session)
```

In **shadow mode** that's where the orchestrator stops. The hook renders the planner system block, computes a sha256 of it, writes a metadata-only JSON line to the shadow log, and returns. The client request is forwarded unchanged.

In **full mode** (not yet shipped) the hook would instead intercept: it would rewrite the upstream request to splice in the planner block, force `tool_choice` to `emit_plan`, parse the returned `plan@v1` program, hand it to the executor, walk the DAG, fabricate any required `client_tool` / `ask_user` blocks back to the client, and finally synthesize an Anthropic-shaped response carrying the terminal step's message.

## Package boundaries

```
internal/orchestrator/
├── orchestrator.go    Façade: load config, render planner block, ExecutePlan/CheckPlan
├── shadow.go          ShadowHook + async JSONL writer (observation only)
├── plan/              plan@v1: lexer (n-token lookahead), parser, AST, semantic check, predicate eval
├── exec/              Executor: DAG walk, runtime bindings env, per-kind step handlers
├── manifest/          YAML loaders for ~/.intern/local_models.yaml and permissions.yaml
├── tools/             Internal tool registry (Bash, Read, Grep, Glob) — used by exec
└── planner/           Embedded prompt template + emit_plan tool definition + extractor
```

The proxy depends on `orchestrator` (the façade); the façade owns everything below it. `plan/` has no dependency on `exec/` or higher — it can be tested in isolation. `exec/` depends on `plan/` (it walks `plan.Plan` ASTs) and `tools/`, but exposes `LocalBackend` / `AgentBackend` / `ClientBridge` interfaces so it doesn't drag in the (still-unimplemented) network backends.

## Configuration

Both files are optional. Missing files fall back to safe defaults:

### `~/.intern/local_models.yaml`

The local model registry. Each entry tells the planner what's available and what each model is good (and bad) at. Empty registry → planner table is replaced with a fallback message and `local` plan steps are unusable.

```yaml
models:
  - id: llama3-8b
    backend: ollama
    endpoint: http://localhost:11434
    model: llama3:8b
    capabilities: [summarize, classify, extract]
    anti_capabilities: [code_generation_multi_file]
    max_input_tokens: 8000
    avg_latency_ms: 800
    description: "General 8B; good for summarization, classification, short extractions."
```

### `~/.intern/permissions.yaml`

Internal tool allowlist. Defaults: `Read`, `Grep`, `Glob` fully permitted; `Bash` restricted to a read-only command set (`ls cat git grep find head tail wc echo test stat file pwd tree diff`). Override to widen or narrow:

```yaml
internal:
  - name: Read
  - name: Grep
  - name: Glob
  - name: Bash
    bash_allowlist: [ls, cat, grep, head, tail, wc, git, find]
```

## Manifest hash

The orchestrator computes a stable hash over `(canonicalized local_models.yaml, planner_template_version)` and pins it onto every plan it generates. The hash:

- Goes into the rendered planner block, so plans Opus emits include `meta.manifest: 0x...`.
- Is checked at plan validation time. A drifted hash (manifest edited after a plan was emitted) is rejected with category `manifest_mismatch`, forcing a fresh plan rather than executing one that referenced the wrong tool/model set.
- Provides the prompt-cache contract: the planner block bytes are stable for a given hash, so Anthropic's prompt cache hits on every reuse within the 1-hour TTL window.

## Shadow mode

```bash
intern proxy --orchestrate=shadow --shadow-log ~/.intern/shadow.jsonl
```

What happens per request:

1. Proxy parses the request body, derives `session_id` from `sha256(messages[0])`.
2. `ShadowHook.OnRequest` fires. If we've already logged this session, return immediately.
3. Otherwise, append one JSONL line:

   ```json
   {
     "timestamp": "2026-05-02T04:22:27Z",
     "session_id": "461cb7b6c0169e34",
     "model": "claude-opus-4-7",
     "manifest_hash": "0x890bc8a9d34ba5df",
     "planner_bytes": 14936,
     "planner_sha256_8": "0x3a963ae9daa1e7a7",
     "emit_plan_tool_name": "emit_plan",
     "emit_plan_tool_json": "{ ... full schema ... }"
   }
   ```

4. Forward the request to Anthropic unchanged.

What it validates without spending any API budget:

- Manifest + permissions YAML files load cleanly
- Planner block renders without missing placeholders
- Planner bytes are stable across requests (`planner_sha256_8` should be identical for every line as long as the manifest hasn't changed)
- Session deduplication works (one line per session, not per request)
- The hook doesn't break the proxy hot path

## Verifying the rendered prompt

```bash
intern plan                # full block to stdout
intern plan | wc -c        # byte size
intern plan --json 2>&1 1>/dev/null  # emit_plan tool schema only
```

The output is what shadow mode would log the size of, and what full mode would prepend to the upstream `system` block. Paste it into a Claude Console session to manually exercise the planner before flipping the switch.

## Plan execution

The executor (`internal/orchestrator/exec`) walks a parsed-and-checked `*plan.Plan` from `meta.entry`, dispatching per step kind:

| Kind | Status | Notes |
|---|---|---|
| `terminal` | ✅ shipped | Final message; ends the run |
| `internal_tool` | ✅ shipped | Calls `tools.Registry` under permissions |
| `branch` | ✅ shipped | Predicate via `plan.Eval` |
| `parallel` | ✅ shipped | Concurrent fan-out, mutex-guarded shared env, cancel on error |
| `client_tool` | 🔌 plug-point | `ClientBridge.RunClientTool` — interface defined, no impl |
| `local` | 🔌 plug-point | `LocalBackend.RunLocal` |
| `agent` | 🔌 plug-point | `AgentBackend.RunAgent` |
| `ask_user` | 🔌 plug-point | `ClientBridge.AskUser` |
| `escalate` | ✅ shipped (unwind) | Returns `*EscalationRequest`; caller phones planner |

Step errors fall through to a per-step `catch =>` handler, then `meta.on_err`, and finally bubble up. Inside catch handlers the auto-bound `$err` record carries `.step`, `.msg`, `.code`.

## Testing

```bash
go test ./internal/orchestrator/...
```

Notable test groups:
- `plan/lexer_test.go`, `plan/parser_test.go`, `plan/check_test.go`, `plan/eval_test.go` — grammar correctness
- `exec/exec_test.go` — runtime, including parallel-with-race-detector
- `planner/planner_test.go` — placeholder substitution + a cross-validation test that **parses and checks every worked example** in the rendered template. This guards against drift between the docs and the grammar implementation.
- `orchestrator_test.go` — façade end-to-end + shadow hook write/dedup/nil-safe

## Why shadow first

Building this right means getting the prompt-cache contract right — and that's the kind of contract that's easy to break silently. Shadow mode is the harness for verifying:

1. **Byte stability** — Across every request in a session, the planner block hashes to the same prefix. If two consecutive shadow log lines disagree on `planner_sha256_8`, prompt-cache won't hit and the cost story collapses. Catch it before it costs anything.
2. **Manifest hash correctness** — Configuration changes invalidate the hash exactly when they should (and never when they shouldn't).
3. **Hook performance** — The observer adds zero blocking work on the request path. If the shadow buffer fills up, entries drop with a log line rather than backpressuring the proxy.
4. **Configuration discoverability** — Users can drop YAML files into `~/.intern/` and watch the planner block contents change without burning a single API call.

Once these invariants are proven on real sessions, full-mode wiring (request rewrite → executor → response synthesis) becomes mostly mechanical.
