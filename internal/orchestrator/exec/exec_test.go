package exec

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
	"github.com/abhishekjha17/intern/internal/orchestrator/tools"
)

// runPlan parses, checks, and executes src against ex; t.Fatal on parse
// or check errors so tests focus on runtime behavior.
func runPlan(t *testing.T, ex *Executor, src string) (*Result, error) {
	t.Helper()
	p, err := plan.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if errs := plan.Check(p, plan.CheckContext{}); len(errs) > 0 {
		plan.SetSource(errs, src)
		t.Fatalf("check: %v", errs[0])
	}
	return ex.Run(context.Background(), p)
}

// ---- Terminal + interpolation -------------------------------------------

func TestRun_TerminalLiteral(t *testing.T) {
	src := `plan @v1 {
		meta { entry: t }
		t := terminal "done"
	}`
	res, err := runPlan(t, &Executor{}, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "done" {
		t.Errorf("terminal = %q, want %q", res.Terminal, "done")
	}
	if len(res.Records) != 1 || res.Records[0].Kind != "terminal" {
		t.Errorf("records=%+v", res.Records)
	}
}

func TestRun_TerminalInterpolation(t *testing.T) {
	// const → terminal interpolation. We build the env up via a const decl
	// rather than a tool result so the test is dependency-free.
	src := `plan @v1 {
		meta { entry: t }
		const greeting := "hi"
		t := terminal "${greeting} world"
	}`
	res, err := runPlan(t, &Executor{}, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "hi world" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

// ---- Internal tool dispatch --------------------------------------------

func TestRun_InternalTool_Bash(t *testing.T) {
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}

	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "echo hello"} -> $r => s2
		s2 := terminal "${r.stdout}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "hello\n" {
		t.Errorf("terminal = %q, want \"hello\\n\"", res.Terminal)
	}
	if len(res.Records) != 2 {
		t.Errorf("expected 2 records, got %d", len(res.Records))
	}
}

func TestRun_InternalTool_NoRegistry(t *testing.T) {
	ex := &Executor{} // no Tools wired
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "echo hi"} -> $r => s2
		s2 := terminal "${r.stdout}"
	}`
	_, err := runPlan(t, ex, src)
	if !errors.Is(err, ErrToolsNotConfigured) {
		t.Errorf("expected ErrToolsNotConfigured, got %v", err)
	}
}

// ---- Branch -----------------------------------------------------------

func TestRun_Branch_TrueArm(t *testing.T) {
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}

	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "echo hello"} -> $r => s2
		s2 := branch (contains $r.stdout "hello") => yes | no
		yes := terminal "matched"
		no  := terminal "didnt"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "matched" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

func TestRun_Branch_FalseArm(t *testing.T) {
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "echo nope"} -> $r => s2
		s2 := branch (contains $r.stdout "hello") => yes | no
		yes := terminal "matched"
		no  := terminal "didnt"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "didnt" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

// ---- Catch and on_err --------------------------------------------------

func TestRun_CatchHandler(t *testing.T) {
	// Use an internal_tool with a denied command so it errors, then the
	// catch handler binds $err and routes us to a terminal that reads it.
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "rm -rf /"} -> $r catch => recover => done
		done    := terminal "ran"
		recover := terminal "caught: ${err.step}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "caught: s1" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

func TestRun_OnErrFallback(t *testing.T) {
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}
	// No catch on s1 — falls through to meta.on_err.
	src := `plan @v1 {
		meta {
			entry: s1
			on_err: fallback
		}
		s1 := internal_tool Bash {cmd: "rm -rf /"} -> $r => done
		done     := terminal "ran"
		fallback := terminal "fell back from ${err.step}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "fell back from s1" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

// ---- Parallel ---------------------------------------------------------

// stubLocal returns a fixed string per call; counts invocations to verify
// concurrency.
type stubLocal struct {
	calls int32
	out   string
	delay time.Duration
}

func (s *stubLocal) RunLocal(ctx context.Context, req LocalRequest) (LocalResult, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return LocalResult{}, ctx.Err()
		}
	}
	return LocalResult{Output: s.out + ":" + req.ModelID}, nil
}

func TestRun_Parallel_FanOut(t *testing.T) {
	local := &stubLocal{out: "ok"}
	// Use a manifest with a known model id so the checker doesn't reject.
	ex := &Executor{Local: local}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := parallel [a, b, c] => merge
		a := local m "first"  -> $ra => merge
		b := local m "second" -> $rb => merge
		c := local m "third"  -> $rc => merge
		merge := terminal "${ra}|${rb}|${rc}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if local.calls != 3 {
		t.Errorf("expected 3 local calls, got %d", local.calls)
	}
	if res.Terminal != "ok:m|ok:m|ok:m" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

func TestRun_Parallel_ChildError(t *testing.T) {
	// One child errors; runParallel reports the error and the run aborts.
	ex := &Executor{Local: errLocal{}}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := parallel [a, b] => merge
		a := local m "ok"  -> $ra => merge
		b := local m "ok"  -> $rb => merge
		merge := terminal "${ra}|${rb}"
	}`
	_, err := runPlan(t, ex, src)
	if err == nil {
		t.Fatal("expected error from parallel child")
	}
}

type errLocal struct{}

func (errLocal) RunLocal(context.Context, LocalRequest) (LocalResult, error) {
	return LocalResult{}, errors.New("boom")
}

// ---- Local backend ----------------------------------------------------

func TestRun_Local_Linear(t *testing.T) {
	local := &stubLocal{out: "summary"}
	ex := &Executor{Local: local}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := local m "explain ${input}" iter=2 timeout_ms=1000 -> $out => s2
		s2 := terminal "${out}"
	}`
	// Seed input via a const so interpolation has something to read.
	src = `plan @v1 {
		meta { entry: s1 }
		const input := "the thing"
		s1 := local m "explain ${input}" iter=2 timeout_ms=1000 -> $out => s2
		s2 := terminal "${out}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "summary:m" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

// ---- Agent + Client bridge stubs --------------------------------------

type stubAgent struct{ out string }

func (s stubAgent) RunAgent(_ context.Context, req AgentRequest) (AgentResult, error) {
	return AgentResult{Output: s.out + "/" + req.Profile}, nil
}

type stubClient struct {
	answer       string
	clientTool   func(req ClientToolRequest) (any, error)
	escalateErr  error
	calledClient bool
	calledAsk    bool
}

func (s *stubClient) RunClientTool(_ context.Context, req ClientToolRequest) (any, error) {
	s.calledClient = true
	if s.clientTool != nil {
		return s.clientTool(req)
	}
	return "client-result", nil
}

func (s *stubClient) AskUser(_ context.Context, req AskUserRequest) (string, error) {
	s.calledAsk = true
	return s.answer, nil
}

func (s *stubClient) Escalate(context.Context, EscalateRequest) error {
	return s.escalateErr
}

func TestRun_Agent_Linear(t *testing.T) {
	ex := &Executor{Agent: stubAgent{out: "review"}}
	src := `plan @v1 {
		meta { entry: a1 }
		a1 := agent code-reviewer "look at this" -> $rv => term
		term := terminal "${rv}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Terminal != "review/code-reviewer" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

func TestRun_AskUser_FreeText(t *testing.T) {
	c := &stubClient{answer: "the answer"}
	ex := &Executor{Client: c}
	src := `plan @v1 {
		meta { entry: q }
		q := ask_user "where is the file?" -> $a => term
		term := terminal "you said ${a}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !c.calledAsk {
		t.Error("AskUser was not called")
	}
	if res.Terminal != "you said the answer" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

func TestRun_ClientTool_RoundTrip(t *testing.T) {
	c := &stubClient{
		clientTool: func(req ClientToolRequest) (any, error) {
			// Verify the tool name and that args round-tripped through JSON.
			if req.Name != "Edit" {
				return nil, errors.New("wrong tool")
			}
			var got map[string]any
			if err := json.Unmarshal(req.Args, &got); err != nil {
				return nil, err
			}
			if got["file_path"] != "x.go" {
				return nil, errors.New("file_path not interpolated")
			}
			return "edited", nil
		},
	}
	ex := &Executor{Client: c}
	src := `plan @v1 {
		meta { entry: s1 }
		const f := "x.go"
		s1 := client_tool Edit {file_path: "${f}", old_string: "a", new_string: "b"} -> $r => term
		term := terminal "${r}"
	}`
	res, err := runPlan(t, ex, src)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !c.calledClient {
		t.Error("RunClientTool was not called")
	}
	if res.Terminal != "edited" {
		t.Errorf("terminal = %q", res.Terminal)
	}
}

// ---- Escalate ---------------------------------------------------------

func TestRun_Escalate_Unwinds(t *testing.T) {
	ex := &Executor{}
	src := `plan @v1 {
		meta { entry: s1 }
		const cause := "stuck"
		s1 := escalate "need help: ${cause}"
		term := terminal "should not run"
	}`
	res, err := runPlan(t, ex, src)
	if err == nil {
		t.Fatal("expected escalation error")
	}
	er := &EscalationRequest{}
	if !errors.As(err, &er) {
		t.Fatalf("error not *EscalationRequest: %T %v", err, err)
	}
	if er.Reason != "need help: stuck" {
		t.Errorf("reason = %q", er.Reason)
	}
	if res.Terminal != "" {
		t.Errorf("terminal should be empty on escalation, got %q", res.Terminal)
	}
}

func TestRun_Escalate_WithCtxAndResume(t *testing.T) {
	reg := tools.NewRegistry(manifest.DefaultPermissions())
	ex := &Executor{Tools: reg}
	src := `plan @v1 {
		meta { entry: s1 }
		s1 := internal_tool Bash {cmd: "echo data"} -> $d => s2
		s2 := escalate "need more" ctx=[$d] => resume
		resume := terminal "resumed"
	}`
	_, err := runPlan(t, ex, src)
	er := &EscalationRequest{}
	if !errors.As(err, &er) {
		t.Fatalf("not escalation: %v", err)
	}
	if er.Resume != "resume" {
		t.Errorf("resume = %q, want \"resume\"", er.Resume)
	}
	if _, ok := er.Context["d"]; !ok {
		t.Errorf("ctx missing $d: %+v", er.Context)
	}
}

// ---- Interpolation edge cases -----------------------------------------

func TestInterpolate_UnknownVar(t *testing.T) {
	env := newEnv()
	_, err := interpolate("hello ${missing}", env)
	if err == nil {
		t.Fatal("expected error for unbound var")
	}
}

func TestInterpolate_LiteralDollar(t *testing.T) {
	// `\$` is processed by the lexer to a literal `$`; by the time interpolate
	// sees the string, an unescaped `${` is always an interp.
	env := newEnv()
	got, err := interpolate("price: $5", env)
	if err != nil {
		t.Fatalf("interp: %v", err)
	}
	if got != "price: $5" {
		t.Errorf("got %q", got)
	}
}

func TestInterpolate_FieldChain(t *testing.T) {
	env := newEnv()
	env.set("r", &plan.ExecResult{Stdout: "out", Stderr: "err", Exit: 1})
	got, err := interpolate("stdout=${r.stdout} exit=${r.exit}", env)
	if err != nil {
		t.Fatalf("interp: %v", err)
	}
	if got != "stdout=out exit=1" {
		t.Errorf("got %q", got)
	}
}
