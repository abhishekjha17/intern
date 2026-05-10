package exec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
)

// runInternalTool dispatches to the tools registry with permission gating
// applied by the registry. The bound output is whatever the tool returns
// (e.g. *plan.ExecResult for Bash, string for Read), stored unboxed so
// predicates can read .stdout / .exit field paths via reflect.
func (ex *Executor) runInternalTool(ctx context.Context, s *plan.InternalToolStep, env *runtimeEnv) (string, error) {
	if ex.Tools == nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), ErrToolsNotConfigured)
	}
	args, err := objToJSON(s.Args, env)
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	out, err := ex.Tools.Run(ctx, s.Tool.Name, args)
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	if v := s.OutBind(); v != "" {
		env.set(v, out)
	}
	return s.Control()[0], nil
}

// runClientTool routes a state-mutating tool call through the client
// bridge so the user's IDE sees and can decline it. Surface defaults to
// "client" for client_tool steps; the tool ref's Surface, if set,
// overrides (the planner uses ":internal" to force a known-mutating tool
// onto the silent path against convention — generally a mistake, but the
// grammar allows it for symmetry).
func (ex *Executor) runClientTool(ctx context.Context, s *plan.ClientToolStep, env *runtimeEnv) (string, error) {
	if ex.Client == nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), ErrClientNotConfigured)
	}
	args, err := objToJSON(s.Args, env)
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	out, err := ex.Client.RunClientTool(ctx, ClientToolRequest{
		Name:    s.Tool.Name,
		Surface: s.Tool.Surface,
		Args:    args,
	})
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	if v := s.OutBind(); v != "" {
		env.set(v, out)
	}
	return s.Control()[0], nil
}

// runLocal invokes the LocalBackend with the prompt interpolated against
// env and the kwarg-declared budget. The backend is responsible for the
// internal agent loop (tool calls, iter cap, timeout). format=json
// causes the bound value to be the decoded JSON, not the raw string.
func (ex *Executor) runLocal(ctx context.Context, s *plan.LocalStep, env *runtimeEnv) (string, error) {
	if ex.Local == nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), ErrLocalNotConfigured)
	}
	prompt, err := interpolate(s.Prompt, env)
	if err != nil {
		return "", fmt.Errorf("step %s prompt: %w", s.StepID(), err)
	}
	iter, err := kwArgInt(s.KWArgs, "iter", 3)
	if err != nil {
		return "", err
	}
	timeoutMS, err := kwArgInt(s.KWArgs, "timeout_ms", 30_000)
	if err != nil {
		return "", err
	}
	format, err := kwArgString(s.KWArgs, "format", "text", env)
	if err != nil {
		return "", err
	}
	tools, err := localToolList(s.KWArgs, env)
	if err != nil {
		return "", err
	}
	out, err := ex.Local.RunLocal(ctx, LocalRequest{
		ModelID: s.ModelID,
		Prompt:  prompt,
		Tools:   tools,
		Iter:    iter,
		Timeout: time.Duration(timeoutMS) * time.Millisecond,
		Format:  format,
	})
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	if v := s.OutBind(); v != "" {
		env.set(v, out.Output)
	}
	return s.Control()[0], nil
}

// localToolList resolves the `tools=[...]` kwarg into a flat list of
// "Name" or "Name:surface" strings for the LocalBackend. Surface
// defaulting (read-only → :internal, mutating → :client) is the
// backend's responsibility, but we pass through the declared form
// verbatim so the backend has the full information.
func localToolList(kw plan.KWArgs, env *runtimeEnv) ([]string, error) {
	v, ok := kw.Get("tools")
	if !ok {
		return nil, nil
	}
	if v.Kind != plan.ValueList {
		return nil, fmt.Errorf("local.tools must be a list")
	}
	out := make([]string, 0, len(v.List))
	for _, item := range v.List {
		switch item.Kind {
		case plan.ValueIdent:
			out = append(out, item.Ident)
		case plan.ValueString:
			s, err := interpolate(item.Str, env)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		default:
			return nil, fmt.Errorf("local.tools entries must be tool refs (got %v)", item.Kind)
		}
	}
	return out, nil
}

// runAgent invokes the AgentBackend with the prompt interpolated and any
// declared inputs collected as a name→value snapshot. The backend
// fabricates the Task tool_use back to Claude Code; the bound output is
// the subagent's final report.
func (ex *Executor) runAgent(ctx context.Context, s *plan.AgentStep, env *runtimeEnv) (string, error) {
	if ex.Agent == nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), ErrAgentNotConfigured)
	}
	prompt, err := interpolate(s.Prompt, env)
	if err != nil {
		return "", fmt.Errorf("step %s prompt: %w", s.StepID(), err)
	}
	inputs, err := agentInputs(s.KWArgs, env)
	if err != nil {
		return "", err
	}
	maxIter, err := kwArgInt(s.KWArgs, "max_iter", 8)
	if err != nil {
		return "", err
	}
	timeoutMS, err := kwArgInt(s.KWArgs, "timeout_ms", 0)
	if err != nil {
		return "", err
	}
	out, err := ex.Agent.RunAgent(ctx, AgentRequest{
		Profile: s.Profile,
		Prompt:  prompt,
		Inputs:  inputs,
		MaxIter: maxIter,
		Timeout: time.Duration(timeoutMS) * time.Millisecond,
	})
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	if v := s.OutBind(); v != "" {
		env.set(v, out.Output)
	}
	return s.Control()[0], nil
}

// agentInputs resolves `inputs=[$v1, $v2, ...]` into a map keyed by the
// var name (without the `$`). Field-chained refs (`$x.y`) collapse to the
// dotted name as the key.
func agentInputs(kw plan.KWArgs, env *runtimeEnv) (map[string]any, error) {
	v, ok := kw.Get("inputs")
	if !ok {
		return nil, nil
	}
	if v.Kind != plan.ValueList {
		return nil, fmt.Errorf("agent.inputs must be a list")
	}
	out := make(map[string]any, len(v.List))
	for _, item := range v.List {
		if item.Kind != plan.ValueVar || item.Var == nil {
			return nil, fmt.Errorf("agent.inputs entries must be variable references")
		}
		got, ok := env.Lookup(item.Var.Name, item.Var.Fields)
		if !ok {
			return nil, fmt.Errorf("agent input $%s not bound", item.Var)
		}
		key := item.Var.Name
		if len(item.Var.Fields) > 0 {
			key = key + "." + strings.Join(item.Var.Fields, ".")
		}
		out[key] = got
	}
	return out, nil
}

// runAskUser fabricates an AskUserQuestion tool_use through the client
// bridge and binds the user's reply. choices=[...] surfaces as discrete
// options; absence yields a free-text response.
func (ex *Executor) runAskUser(ctx context.Context, s *plan.AskUserStep, env *runtimeEnv) (string, error) {
	if ex.Client == nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), ErrClientNotConfigured)
	}
	q, err := interpolate(s.Question, env)
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	var choices []string
	if list, ok, err := kwArgList(s.KWArgs, "choices", env); err != nil {
		return "", err
	} else if ok {
		choices = make([]string, 0, len(list))
		for _, c := range list {
			choices = append(choices, stringify(c))
		}
	}
	answer, err := ex.Client.AskUser(ctx, AskUserRequest{Question: q, Choices: choices})
	if err != nil {
		return "", fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	if v := s.OutBind(); v != "" {
		env.set(v, answer)
	}
	return s.Control()[0], nil
}

// runEscalate captures the declared ctx vars and unwinds the run with an
// EscalationRequest. The orchestrator above this loop is responsible for
// phoning the planner with these vars and (if Resume is set) re-entering
// the plan at that step ID with the sub-plan's bindings merged in.
func (ex *Executor) runEscalate(s *plan.EscalateStep, env *runtimeEnv) error {
	reason, err := interpolate(s.Reason, env)
	if err != nil {
		return fmt.Errorf("step %s: %w", s.StepID(), err)
	}
	ctxVars, err := escalateContext(s.KWArgs, env)
	if err != nil {
		return err
	}
	resume := ""
	if c := s.Control(); len(c) == 1 {
		resume = c[0]
	}
	return &EscalationRequest{Reason: reason, Context: ctxVars, Resume: resume}
}

// escalateContext collects the `ctx=[$v, ...]` var snapshot the planner
// needs to make its replanning decision.
func escalateContext(kw plan.KWArgs, env *runtimeEnv) (map[string]any, error) {
	v, ok := kw.Get("ctx")
	if !ok {
		return nil, nil
	}
	if v.Kind != plan.ValueList {
		return nil, fmt.Errorf("escalate.ctx must be a list")
	}
	out := make(map[string]any, len(v.List))
	for _, item := range v.List {
		if item.Kind != plan.ValueVar || item.Var == nil {
			return nil, fmt.Errorf("escalate.ctx entries must be variable references")
		}
		got, ok := env.Lookup(item.Var.Name, item.Var.Fields)
		if !ok {
			return nil, fmt.Errorf("escalate.ctx $%s not bound", item.Var)
		}
		key := item.Var.Name
		if len(item.Var.Fields) > 0 {
			key = key + "." + strings.Join(item.Var.Fields, ".")
		}
		out[key] = got
	}
	return out, nil
}

// Compile-time check: runtimeEnv satisfies plan.Bindings so the predicate
// evaluator can take *runtimeEnv directly.
var _ plan.Bindings = (*runtimeEnv)(nil)
