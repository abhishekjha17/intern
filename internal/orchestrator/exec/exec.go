// Package exec walks a parsed and semantically-checked plan@v1 program,
// dispatching each step to its handler and threading bound outputs through
// a shared runtime environment. Construct one Executor per plan run; the
// type is not safe for concurrent reuse, but parallel-step children inside
// a single Run are scheduled concurrently against a mutex-guarded env.
package exec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
	"github.com/abhishekjha17/intern/internal/orchestrator/tools"
)

// Executor wires a parsed plan against the runtime surfaces it needs:
// the internal tools registry, the local-model and agent backends, the
// client bridge for user-visible tool calls, and the predicate classifier
// hook for `(classify ...)` predicates. Any of the backend fields may be
// left nil; the matching step kinds will return a "not configured" error
// instead of panicking, so a partially-configured Executor still runs the
// step kinds whose dependencies are wired.
type Executor struct {
	Tools      *tools.Registry
	Local      LocalBackend
	Agent      AgentBackend
	Client     ClientBridge
	Classifier plan.Classifier
}

// Result captures the outcome of a plan run. Terminal carries the
// interpolated final message of the terminal step that ended the walk;
// it is empty when an EscalationRequest unwound the run instead.
// Records lists every step that executed, in completion order.
// Bindings exposes the live runtime env, primarily for tests that want
// to assert intermediate values; production callers should rely on
// Terminal and Records.
type Result struct {
	Terminal string
	Records  []StepRecord
	Bindings map[string]any
}

// StepRecord is one row of the execution log. Err is nil when the step
// ran cleanly; on a step error the orchestrator's catch / on_err logic
// runs next and a follow-up record captures that handler.
type StepRecord struct {
	ID   string
	Kind string
	Took time.Duration
	Err  error
}

// EscalationRequest is the sentinel error escape that an `escalate` step
// raises to unwind back to the planner for a sub-plan. The proxy turns
// this into a fresh planning call to Opus with the captured context vars,
// then resumes at Resume (if non-empty) once the sub-plan finishes.
type EscalationRequest struct {
	Reason  string
	Context map[string]any
	Resume  string
}

func (e *EscalationRequest) Error() string {
	return fmt.Sprintf("escalation requested at plan: %s", e.Reason)
}

// Run executes p starting at meta.entry. The returned Result is always
// non-nil; even on error its Records slice contains the step trail up to
// the failure point, which is useful for diagnostics. A nil error means
// a terminal step ended the run cleanly.
func (ex *Executor) Run(ctx context.Context, p *plan.Plan) (*Result, error) {
	if p == nil {
		return nil, errors.New("nil plan")
	}

	env := newEnv()
	res := &Result{}
	defer func() { res.Bindings = env.snapshot() }()

	// Seed const declarations as eagerly-evaluated values.
	for _, cd := range p.Consts {
		v, err := evalLiteral(cd.Value, env)
		if err != nil {
			return res, fmt.Errorf("const %s: %w", cd.Name, err)
		}
		env.set(cd.Name, v)
	}

	stepByID := map[string]plan.Step{}
	for _, s := range p.Steps {
		stepByID[s.StepID()] = s
	}

	entryV, ok := p.Meta.Fields["entry"]
	if !ok || entryV.Kind != plan.MetaIdent {
		return res, errors.New("plan meta missing 'entry' field")
	}
	onErr := ""
	if v, ok := p.Meta.Fields["on_err"]; ok && v.Kind == plan.MetaIdent {
		onErr = v.Ident
	}

	cur := entryV.Ident
	for cur != "" {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		s, ok := stepByID[cur]
		if !ok {
			return res, fmt.Errorf("control flow references unknown step %q", cur)
		}

		start := time.Now()
		next, err := ex.runStep(ctx, s, env, stepByID)
		res.Records = append(res.Records, StepRecord{
			ID:   s.StepID(),
			Kind: s.StepKind(),
			Took: time.Since(start),
			Err:  err,
		})

		if err != nil {
			// Escalation propagates immediately — the caller phones the planner.
			if er := (*EscalationRequest)(nil); errors.As(err, &er) {
				return res, err
			}
			// catch on the failing step takes precedence over meta.on_err.
			if catch := s.Catch(); catch != "" {
				bindErrVar(env, s.StepID(), err)
				cur = catch
				continue
			}
			if onErr != "" {
				bindErrVar(env, s.StepID(), err)
				cur = onErr
				continue
			}
			return res, err
		}

		// Terminal sets res.Terminal and signals exit by returning "".
		if t, ok := s.(*plan.TerminalStep); ok {
			msg, ierr := interpolate(t.Message, env)
			if ierr != nil {
				return res, fmt.Errorf("terminal: %w", ierr)
			}
			res.Terminal = msg
		}
		cur = next
	}

	return res, nil
}

// runStep dispatches one step. The returned next-step ID is "" on
// terminal/escalate (no resume) or when the step kind has no successor;
// a non-empty string is the next ID the walker should jump to.
func (ex *Executor) runStep(ctx context.Context, s plan.Step, env *runtimeEnv, stepByID map[string]plan.Step) (string, error) {
	switch v := s.(type) {
	case *plan.TerminalStep:
		// Terminal text interpolation happens in the Run loop after the
		// Records entry is appended (so a failing interp still records
		// the step). Returning "" signals exit.
		return "", nil

	case *plan.InternalToolStep:
		return ex.runInternalTool(ctx, v, env)

	case *plan.ClientToolStep:
		return ex.runClientTool(ctx, v, env)

	case *plan.BranchStep:
		ok, err := plan.Eval(v.Pred, env, ex.Classifier)
		if err != nil {
			return "", fmt.Errorf("branch %s: %w", v.StepID(), err)
		}
		if ok {
			return v.Control()[0], nil
		}
		return v.Control()[1], nil

	case *plan.ParallelStep:
		if err := ex.runParallel(ctx, v, env, stepByID); err != nil {
			return "", err
		}
		return v.Control()[0], nil

	case *plan.LocalStep:
		return ex.runLocal(ctx, v, env)

	case *plan.AgentStep:
		return ex.runAgent(ctx, v, env)

	case *plan.AskUserStep:
		return ex.runAskUser(ctx, v, env)

	case *plan.EscalateStep:
		return "", ex.runEscalate(v, env)
	}
	return "", fmt.Errorf("unhandled step kind %T", s)
}

// runParallel fans out children concurrently against the shared env,
// joins, and returns the first error (if any). Children's outputs bind
// into env via runStep's own outBind handling; the env's mutex serializes
// reads/writes.
func (ex *Executor) runParallel(ctx context.Context, p *plan.ParallelStep, env *runtimeEnv, stepByID map[string]plan.Step) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(p.Children))

	for i, childID := range p.Children {
		child, ok := stepByID[childID]
		if !ok {
			return fmt.Errorf("parallel %s: unknown child %q", p.StepID(), childID)
		}
		wg.Add(1)
		go func(i int, s plan.Step) {
			defer wg.Done()
			if _, err := ex.runStep(cctx, s, env, stepByID); err != nil {
				errs[i] = fmt.Errorf("parallel child %s: %w", s.StepID(), err)
				cancel()
			}
		}(i, child)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// bindErrVar populates the reserved $err record on a step failure so
// catch handlers and meta.on_err steps can read $err.step / $err.msg.
// Code is reserved for future use (e.g. mapping tool errors to numeric codes).
func bindErrVar(env *runtimeEnv, stepID string, err error) {
	env.set("err", map[string]any{
		"step": stepID,
		"msg":  err.Error(),
		"code": "exec_error",
	})
}

// ---- Runtime environment -----------------------------------------------

// runtimeEnv is a concurrent-safe variable environment satisfying
// plan.Bindings. Parallel-step children share one env; the mutex
// serializes the per-key writes (different children write different keys
// by construction of the v1 grammar's binding rules) and any concurrent
// reads from predicates inside children.
type runtimeEnv struct {
	mu  sync.RWMutex
	env map[string]any
}

func newEnv() *runtimeEnv { return &runtimeEnv{env: map[string]any{}} }

func (e *runtimeEnv) set(name string, v any) {
	e.mu.Lock()
	e.env[name] = v
	e.mu.Unlock()
}

// Lookup implements plan.Bindings — predicate evaluator queries this for
// $name[.field] reads.
func (e *runtimeEnv) Lookup(name string, fields []string) (any, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return plan.MapBindings(e.env).Lookup(name, fields)
}

// snapshot returns a shallow copy of the live env, suitable for handing
// back to a caller after the Run loop ends. Values are aliased — callers
// must not mutate them, but the map itself is independent of the env.
func (e *runtimeEnv) snapshot() map[string]any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]any, len(e.env))
	for k, v := range e.env {
		out[k] = v
	}
	return out
}
