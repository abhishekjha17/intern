package exec

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// LocalBackend executes a `local <model_id> "<prompt>"` step. Implementations
// dispatch to Ollama / llama-cpp / a generic HTTP backend based on the
// manifest entry the model_id resolves to. The backend is responsible for
// honoring the iter, timeout, and tool budget — the Executor passes them
// through and trusts the backend to enforce.
type LocalBackend interface {
	RunLocal(ctx context.Context, req LocalRequest) (LocalResult, error)
}

// LocalRequest is the LocalBackend input. Tools is the resolved list of
// tool names with surface suffixes the local model is allowed to call
// (`Read`, `Bash:client`, etc.); the backend should validate against its
// own surface conventions and refuse anything not advertised here.
type LocalRequest struct {
	ModelID string
	Prompt  string
	Tools   []string
	Iter    int
	Timeout time.Duration
	Format  string // "text" | "json"
}

// LocalResult carries the bound output. For Format=="json", Output is a
// decoded JSON value (map[string]any / []any / etc.); for "text" it is a
// plain string. Predicates and downstream interpolation operate on Output.
type LocalResult struct {
	Output any
}

// AgentBackend spawns a Claude Code subagent (Task tool) for an `agent`
// step. The orchestrator fabricates the Task tool_use back to the client
// and waits for the corresponding tool_result. Implementations must
// surface failures (subagent errored, timed out) as errors so catch
// handlers can run.
type AgentBackend interface {
	RunAgent(ctx context.Context, req AgentRequest) (AgentResult, error)
}

// AgentRequest is the AgentBackend input. Inputs is the resolved snapshot
// of var refs the planner declared via `inputs=[$v, ...]`; the backend
// flattens them into the Task tool's prompt context.
type AgentRequest struct {
	Profile string
	Prompt  string
	Inputs  map[string]any
	MaxIter int
	Timeout time.Duration
}

// AgentResult is the subagent's final report. The Executor binds Output
// onto the step's outBind variable.
type AgentResult struct {
	Output string
}

// ClientBridge fabricates user-visible tool_use blocks back to the IDE
// (Claude Code, Cursor) for `client_tool`, `ask_user`, and `escalate`
// steps. It is the single seam where the orchestrator crosses from
// invisible internal work to user-witnessed work; everything that goes
// through this interface is something the user sees and can decline.
type ClientBridge interface {
	RunClientTool(ctx context.Context, req ClientToolRequest) (any, error)
	AskUser(ctx context.Context, req AskUserRequest) (string, error)
	Escalate(ctx context.Context, req EscalateRequest) error
}

// ClientToolRequest is the ClientBridge input for `client_tool` steps.
// Args is the plan's obj_literal flattened to JSON, with all string
// interpolations resolved against the runtime env.
type ClientToolRequest struct {
	Name    string
	Surface string // "client" or "" — empty means default for the tool kind
	Args    json.RawMessage
}

// AskUserRequest is the ClientBridge input for `ask_user` steps. Choices
// may be empty (free-text response).
type AskUserRequest struct {
	Question string
	Choices  []string
}

// EscalateRequest is the ClientBridge input for `escalate` steps. The
// bridge returns a typed *EscalationRequest error which the Run loop
// propagates so the proxy can phone the planner for a sub-plan.
type EscalateRequest struct {
	Reason  string
	Context map[string]any
	Resume  string
}

// ---- Default unconfigured-backend errors --------------------------------

// ErrLocalNotConfigured is returned by Executor.runLocal when no
// LocalBackend has been wired. Catch handlers can recover from it; without
// a catch the error propagates and ends the run.
var (
	ErrLocalNotConfigured  = errors.New("exec: no LocalBackend configured")
	ErrAgentNotConfigured  = errors.New("exec: no AgentBackend configured")
	ErrClientNotConfigured = errors.New("exec: no ClientBridge configured")
	ErrToolsNotConfigured  = errors.New("exec: no tools.Registry configured")
)
