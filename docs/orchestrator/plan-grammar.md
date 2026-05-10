# intern plan-grammar v1

The `plan@v1` grammar is the language Opus emits to instruct intern's orchestrator. Plans are emitted via the `emit_plan` tool (Anthropic structured output), parsed by intern's recursive-descent parser, semantically checked, then walked by the orchestrator. Local-model and tool work happens between steps; Opus is invoked only at plan generation and (optionally) on escalation.

This document is the canonical reference for the grammar. Two artifacts derive from it:

1. The Go parser/checker in `internal/orchestrator/plan/` (parses the EBNF below + enforces the semantic rules).
2. The planner system prompt at [`planner-system-prompt.md`](./planner-system-prompt.md) (the prose Opus actually consumes — references this grammar).

## Design Goals

- **Token-cheap.** Emitted plans average ~5x fewer tokens than equivalent JSON.
- **Unambiguous.** A single canonical parse for every well-formed program.
- **DAG-only.** No cycles in v1 — plan walks terminate by structural induction.
- **Closed vocabularies.** Step kinds, predicate ops, and kwargs are enumerated; new entries require a grammar version bump.
- **Compiler-style errors.** Parse and semantic errors carry source spans (`line:col`) so Opus can self-correct on the single retry budget.

## EBNF (ISO 14977 with regex-style ranges)

```ebnf
(* =====================================================
   intern plan-grammar v1
   ===================================================== *)

(* --- top level --- *)
plan          = "plan" "@" version "{" meta { const_decl } step { step } "}" ;
version       = "v" digit { digit } ;

(* --- meta block --- *)
meta          = "meta" "{" meta_field { meta_field } "}" ;
meta_field    = ident ":" meta_value ;
meta_value    = string | number | hex | bool | ident ;

(* --- const declarations --- *)
const_decl    = "const" ident ":=" value ;

(* --- steps --- *)
step          = ident ":=" step_body [ out_bind ] [ control ] [ catch_clause ] ;

step_body     = local_step
              | internal_tool_step
              | client_tool_step
              | branch_step
              | parallel_step
              | agent_step
              | ask_user_step
              | terminal_step
              | escalate_step ;

local_step          = "local"         model_id      string [ kw_args ] ;
internal_tool_step  = "internal_tool" tool_ref      obj_literal ;
client_tool_step    = "client_tool"   tool_ref      obj_literal ;
branch_step         = "branch"        predicate ;
parallel_step       = "parallel"      "[" ident_list "]" ;
agent_step          = "agent"         agent_profile string [ kw_args ] ;
ask_user_step       = "ask_user"      string                [ kw_args ] ;
terminal_step       = "terminal"      string ;
escalate_step       = "escalate"      string                [ kw_args ] ;

out_bind      = "->" var_decl ;
control       = "=>" ctrl_targets ;
ctrl_targets  = ident { "|" ident } ;
catch_clause  = "catch" "=>" ident ;

(* --- keyword args --- *)
kw_args       = kw_arg { kw_arg } ;
kw_arg        = ident "=" value ;

(* --- predicates (s-expression form) --- *)
predicate     = pred_atom | pred_compound ;
pred_atom     = "(" pred_op pred_arg { pred_arg } ")" ;
pred_compound = "(" combinator predicate { predicate } ")" ;
combinator    = "and" | "or" | "not" ;
pred_op       = "exit_code_eq" | "exit_code_ne"
              | "regex_match"  | "contains"
              | "len_gt"       | "len_lt"  | "len_eq"
              | "is_empty"     | "is_nonempty"
              | "json_path"    | "eq"      | "ne"
              | "classify" ;
pred_arg      = var_ref | string | number | bool | ident | list_literal ;

(* --- values, refs, literals --- *)
value         = string | number | bool | hex
              | list_literal | obj_literal | var_ref ;
var_ref       = "$" ident { "." ident } ;
var_decl      = "$" ident ;
list_literal  = "[" [ value { "," value } ] "]" ;
obj_literal   = "{" [ kv { "," kv } ] "}" ;
kv            = ident ":" value ;
ident_list    = ident { "," ident } ;

(* --- identifiers and tool refs --- *)
ident         = letter { letter | digit | "_" } ;
model_id      = ( letter | digit ) { letter | digit | "_" | "-" | ":" | "." } ;
tool_ref      = tool_name [ ":" surface ] ;
tool_name     = letter { letter | digit | "_" } ;
surface       = "client" | "internal" ;
agent_profile = letter { letter | digit | "_" | "-" } ;

(* --- numbers, strings, atoms --- *)
number        = [ "-" ] digit { digit } [ "." digit { digit } ] ;
hex           = "0x" hex_digit { hex_digit } ;
bool          = "true" | "false" ;
string        = '"' { string_char | interp_seq | escape_seq } '"' ;
interp_seq    = "${" ident { "." ident } "}" ;
escape_seq    = "\" ( '"' | "\" | "n" | "t" | "$" ) ;
string_char   = ? any unicode codepoint except '"', '\', '$' ? ;

(* --- character classes --- *)
letter        = "a".."z" | "A".."Z" ;
digit         = "0".."9" ;
hex_digit     = digit | "a".."f" | "A".."F" ;

(* --- lexical (stripped before tokenization) --- *)
(* whitespace : ' ' | '\t' | '\n' | '\r' freely between tokens *)
(* comment    : '#' to end of line, treated as whitespace      *)
```

## Semantic Rules (context-sensitive — checked after parse)

CFGs structurally cannot express these. They run as a separate pass before any step executes; failure produces the same one-retry escalation as a parse error.

### Control-flow shape per step kind

| Step kind | `out_bind` | `control` (number of targets) | `catch_clause` |
|---|---|---|---|
| `local`         | required | exactly 1                          | optional |
| `internal_tool` | required | exactly 1                          | optional |
| `client_tool`   | required | exactly 1                          | optional |
| `agent`         | required | exactly 1                          | optional |
| `ask_user`      | required | exactly 1                          | optional |
| `branch`        | forbidden | exactly 2 (`=> a \| b`)           | optional |
| `parallel`      | forbidden | exactly 1 (the merge step)        | optional |
| `escalate`      | forbidden | 0 or 1 (resume target if present) | optional |
| `terminal`      | forbidden | forbidden                          | forbidden |

### Predicate signatures

Each `pred_op` has fixed arity and arg types. Mismatches raise semantic errors.

| Op | Signature | Notes |
|---|---|---|
| `exit_code_eq`  | `(var:exec_result, int)` | `$bash_out.exit` form also accepted |
| `exit_code_ne`  | `(var:exec_result, int)` | |
| `regex_match`   | `(var:string, string)`   | RE2 syntax; invalid pattern → semantic error |
| `contains`      | `(var:string, string)`   | |
| `len_gt`        | `(var:any, int)`         | bytes for string, items for list/json-array |
| `len_lt`        | `(var:any, int)`         | |
| `len_eq`        | `(var:any, int)`         | |
| `is_empty`      | `(var:any)`              | |
| `is_nonempty`   | `(var:any)`              | |
| `json_path`     | `(var:json, string, op, value)` | op ∈ `==`, `!=`, `>`, `<`, `>=`, `<=` |
| `eq`            | `(var, value)`           | type-equal compare |
| `ne`            | `(var, value)`           | |
| `classify`      | `(var, model_id, kw=labels:[string], kw=expected:string)` | invokes a local model; only fuzzy primitive |
| `and`           | `(predicate{1,N})`       | identity for N=1, conjunction otherwise |
| `or`            | `(predicate{1,N})`       | identity for N=1, disjunction otherwise |
| `not`           | `(predicate)`            | |

### Step-kind keyword args (closed allowlist)

| Step | Allowed kwargs |
|---|---|
| `local`         | `tools=[tool_ref...]`, `iter=int (default 3, max 10)`, `timeout_ms=int (default 30000)`, `format=string ("text"\|"json", default "text")` |
| `agent`         | `inputs=[var_ref...]`, `max_iter=int (default 8)`, `timeout_ms=int` |
| `ask_user`      | `choices=[string...]` (omit → free-text response) |
| `escalate`      | `ctx=[var_ref...]` (vars sent to Opus on replan) |

### Reference and binding rules

1. **Step IDs unique within a plan.** Duplicate identifiers are a semantic error.
2. **All `=>` targets resolve.** Forward references allowed.
3. **No cycles.** The graph induced by control edges (`=>`, branch targets, parallel children, parallel→merge edge, catch edges) must be a DAG.
4. **Variables bound once.** `$x` may not appear as `out_bind` in two steps. The DAG property guarantees a single binding executes per run.
5. **Reads dominated by binds.** For every `$x` read in step S, every path from `meta.entry` to S must pass through the step that binds `$x`. Standard data-flow check.
6. **`meta.entry` resolves** to a declared step ID.
7. **`meta.manifest`** must equal the orchestrator's current manifest hash; mismatch rejects the plan immediately (no retry).
8. **Reserved identifier prefix `__`.** Orchestrator-injected steps only.
9. **Reserved variables `$err`, `$err.step`, `$err.msg`, `$err.code`.** Auto-bound inside `catch` handlers; cannot appear as `var_decl`.
10. **`model_id` resolves against the manifest** at plan-validation time. Unknown model → semantic error.
11. **`tool_ref` resolves** against:
    - For `client_tool` and `agent`: the client's advertised tools (sniffed from incoming request's `tools` field at plan time).
    - For `internal_tool`: intern's permission allowlist (`~/.intern/permissions.yaml`).
    - Inside a `local` step's `tools=[...]`: same as above, with surface defaulting per the rule below.

### Tool surface defaults

A `tool_ref` inside a `local` step's `tools=[...]` defaults to:
- `:internal` for read-only tools (`Read`, `Grep`, `Glob`, read-only `Bash` patterns).
- `:client` for state-mutating tools (`Edit`, `Write`, unrestricted `Bash`).

Plan can override explicitly: `tools=[Read, Bash:client]`. Convention exists so the user sees state-mutating actions in their UI even when initiated by a local model.

### Hard clamps

The orchestrator silently clamps these regardless of plan-declared values:

- `meta.max_esc` clamped to `[0, 3]`.
- `meta.max_agents` clamped to `[0, 8]`.
- `meta.max_local` clamped to `[0, 16]`.
- `meta.budget_usd` clamped to `[0.0, 10.0]`.
- `local.iter` clamped to `[1, 10]`.
- `agent.max_iter` clamped to `[1, 20]`.

A plan declaring values outside these ranges is accepted but executed with the clamped values; a warning metric is emitted.

## Versioning

`plan @v1 { ... }` is mandatory. The parser dispatches on version. v1 is **frozen on first ship** — only additive changes (new optional kwargs, new pred_ops behind a feature flag) are allowed. Breaking changes go to v2 and require a manifest hash bump (which forces fresh plans across all sessions, paying one cache-miss cost).

## Error Reporting Contract

Both parser and semantic checker emit errors of this shape:

```
plan@v1 error at line L col C: <category>: <message>
  L | <source line, syntax-highlighted>
    |     ^^^ <pointer>
  hint: <suggested fix>
```

Categories are: `syntax`, `unknown_step_kind`, `unknown_predicate_op`, `arity_mismatch`, `type_mismatch`, `unresolved_reference`, `cycle_detected`, `unknown_kwarg`, `manifest_mismatch`, `reserved_identifier`.

This format is what gets fed back to Opus on the single retry. It is the load-bearing UX of the system: structured, span-pointed, hint-bearing diagnostics make Opus self-correct reliably; opaque errors do not.
