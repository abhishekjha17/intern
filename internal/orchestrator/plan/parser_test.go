package plan

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Plan {
	t.Helper()
	p, err := Parse(src)
	if err != nil {
		t.Fatalf("parse error:\n%s", err.Error())
	}
	return p
}

func mustFail(t *testing.T, src, wantSubstr string) {
	t.Helper()
	_, err := Parse(src)
	if err == nil {
		t.Fatalf("expected parse error containing %q, got nil", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("error %q does not contain %q", err.Error(), wantSubstr)
	}
}

func TestParse_Minimal(t *testing.T) {
	src := `plan @v1 {
  meta { task: "x" max_esc: 0 entry: s1 }
  s1 := terminal "hello"
}`
	p := mustParse(t, src)
	if p.Version != "v1" {
		t.Errorf("version = %q", p.Version)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("steps = %d", len(p.Steps))
	}
	term, ok := p.Steps[0].(*TerminalStep)
	if !ok {
		t.Fatalf("step[0] = %T, want *TerminalStep", p.Steps[0])
	}
	if term.Message != "hello" {
		t.Errorf("message = %q", term.Message)
	}
}

func TestParse_StepKinds(t *testing.T) {
	src := `plan @v1 {
  meta { task: "all kinds" max_esc: 1 manifest: 0xa1 entry: s1 }

  s1 := internal_tool Bash {cmd: "ls"} -> $out => s2

  s2 := local llama3-8b "summarize: ${out}" tools=[Read] iter=2 -> $sum => s3

  s3 := branch (regex_match $sum "ERROR") => s_err | s4

  s4 := client_tool Edit {path: "x.go", old: "a", new: "b"} -> $r => s5

  s5 := parallel [a1, a2] => s_merge

  a1 := agent code-reviewer "review src" inputs=[$sum] -> $r1
  a2 := agent code-reviewer "review tests" inputs=[$sum] -> $r2

  s_merge := ask_user "merge ok?" choices=["yes","no"] -> $c => s6

  s6 := branch (eq $c "yes") => s_ok | s_esc

  s_err := terminal "error: ${sum}"
  s_ok  := terminal "done: ${r1} ${r2}"
  s_esc := escalate "user said no" ctx=[$r1, $r2]
}`
	p := mustParse(t, src)
	if len(p.Steps) != 12 {
		t.Fatalf("steps = %d, want 12", len(p.Steps))
	}

	// Spot-check kinds.
	wants := []string{
		"internal_tool", "local", "branch", "client_tool", "parallel",
		"agent", "agent", "ask_user", "branch", "terminal", "terminal", "escalate",
	}
	gotIDs := []string{}
	for i, want := range wants {
		got := p.Steps[i].StepKind()
		gotIDs = append(gotIDs, p.Steps[i].StepID())
		if got != want {
			t.Errorf("step[%d] = %s, want %s", i, got, want)
		}
	}

	// s2 has model id with dash and number — verify stitched correctly.
	s2 := p.Steps[1].(*LocalStep)
	if s2.ModelID != "llama3-8b" {
		t.Errorf("model id = %q, want llama3-8b", s2.ModelID)
	}
	// Verify kw args were parsed.
	if v, ok := s2.KWArgs.Get("iter"); !ok || v.Num != 2 {
		t.Errorf("iter kwarg missing or wrong: %+v", v)
	}

	// Branch s3 should have two control targets.
	s3 := p.Steps[2].(*BranchStep)
	if len(s3.Control()) != 2 || s3.Control()[0] != "s_err" || s3.Control()[1] != "s4" {
		t.Errorf("branch control = %v", s3.Control())
	}

	// Parallel s5 should have 2 children and 1 control target.
	s5 := p.Steps[4].(*ParallelStep)
	if len(s5.Children) != 2 || s5.Children[0] != "a1" {
		t.Errorf("parallel children = %v", s5.Children)
	}
	if len(s5.Control()) != 1 || s5.Control()[0] != "s_merge" {
		t.Errorf("parallel control = %v", s5.Control())
	}

	// Agent profile with dash.
	a1 := p.Steps[5].(*AgentStep)
	if a1.Profile != "code-reviewer" {
		t.Errorf("agent profile = %q", a1.Profile)
	}
}

func TestParse_PredicatesAllShapes(t *testing.T) {
	src := `plan @v1 {
  meta { task: "preds" entry: s1 }

  s1 := branch (regex_match $x "ERR") => a | b
  a  := branch (and (eq $y 1) (or (contains $z "foo") (not (is_empty $w)))) => c | d
  b  := branch (classify $t llama3-8b labels=["ok","bad"] expected="ok") => c | d
  c  := branch (json_path $j "$.code" == 200) => e | f
  d  := branch (len_gt $list 3) => e | f
  e  := terminal "yep"
  f  := terminal "nope"
}`
	p := mustParse(t, src)

	// First branch is a simple regex_match atom.
	b := p.Steps[0].(*BranchStep)
	atom, ok := b.Pred.(PredAtom)
	if !ok || atom.Op != "regex_match" {
		t.Fatalf("expected PredAtom regex_match, got %T", b.Pred)
	}

	// Second branch is (and ... (or ... (not ...))) — exercise combinators.
	bA := p.Steps[1].(*BranchStep)
	andP, ok := bA.Pred.(PredAnd)
	if !ok || len(andP.Operands) != 2 {
		t.Fatalf("expected PredAnd with 2 operands, got %T", bA.Pred)
	}
	orP, ok := andP.Operands[1].(PredOr)
	if !ok || len(orP.Operands) != 2 {
		t.Fatalf("expected nested PredOr, got %T", andP.Operands[1])
	}
	if _, ok := orP.Operands[1].(PredNot); !ok {
		t.Fatalf("expected nested PredNot, got %T", orP.Operands[1])
	}

	// classify carries kw-pair args.
	bB := p.Steps[2].(*BranchStep)
	classify := bB.Pred.(PredAtom)
	foundLabels, foundExpected := false, false
	for _, a := range classify.Args {
		if a.Kind == PredArgKWPair {
			switch a.KW.Key {
			case "labels":
				foundLabels = true
			case "expected":
				foundExpected = true
			}
		}
	}
	if !foundLabels || !foundExpected {
		t.Errorf("classify missing kw args: labels=%v expected=%v", foundLabels, foundExpected)
	}
}

func TestParse_ConstDecls(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  const ERR_PAT := "ERROR|FAIL"
  const N := 3
  s1 := terminal "${ERR_PAT}"
}`
	p := mustParse(t, src)
	if len(p.Consts) != 2 {
		t.Fatalf("consts = %d", len(p.Consts))
	}
	if p.Consts[0].Name != "ERR_PAT" || p.Consts[0].Value.Str != "ERROR|FAIL" {
		t.Errorf("const 0 = %+v", p.Consts[0])
	}
	if p.Consts[1].Name != "N" || p.Consts[1].Value.Num != 3 {
		t.Errorf("const 1 = %+v", p.Consts[1])
	}
}

func TestParse_VarFieldChain(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := branch (eq $bash.stdout "x") => a | b
  a := terminal "yes"
  b := terminal "no"
}`
	p := mustParse(t, src)
	atom := p.Steps[0].(*BranchStep).Pred.(PredAtom)
	v := atom.Args[0]
	if v.Kind != PredArgVar || v.Var.Name != "bash" || len(v.Var.Fields) != 1 || v.Var.Fields[0] != "stdout" {
		t.Errorf("var field chain wrong: %+v", v.Var)
	}
}

func TestParse_CatchClause(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 on_err: s_err }
  s1 := internal_tool Read {path: "x"} -> $c catch => s_err => s2
  s2 := terminal "${c}"
  s_err := terminal "fail"
}`
	p := mustParse(t, src)
	s1 := p.Steps[0].(*InternalToolStep)
	if s1.Catch() != "s_err" {
		t.Errorf("catch = %q, want s_err", s1.Catch())
	}
	if len(s1.Control()) != 1 || s1.Control()[0] != "s2" {
		t.Errorf("control = %v", s1.Control())
	}
}

func TestParse_ToolSurface(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := local llama3 "x" tools=[Read, Bash:client] -> $r => s2
  s2 := terminal "${r}"
}`
	p := mustParse(t, src)
	s1 := p.Steps[0].(*LocalStep)
	v, _ := s1.KWArgs.Get("tools")
	if v.Kind != ValueList || len(v.List) != 2 {
		t.Fatalf("tools = %+v", v)
	}
	if v.List[0].Kind != ValueIdent || v.List[0].Ident != "Read" {
		t.Errorf("tools[0] = %+v, want ValueIdent Read", v.List[0])
	}
	if v.List[1].Kind != ValueIdent || v.List[1].Ident != "Bash:client" {
		t.Errorf("tools[1] = %+v, want ValueIdent Bash:client", v.List[1])
	}
}

// --- Worked examples from docs/orchestrator/planner-system-prompt.md ---

func TestParse_ExampleA_LinearPipeline(t *testing.T) {
	src := `plan @v1 {
  meta {
    task: "summarize last commit"
    max_esc: 1
    max_agents: 0
    max_local: 4
    manifest: 0xa1b2c3d4
    on_err: s_default_err
    entry: s1
  }

  s1 := internal_tool Bash {cmd: "git diff HEAD~1 --stat"}
        -> $stat
        => s2

  s2 := branch (is_empty $stat.stdout) => s_nochanges | s3

  s3 := local llama3-8b "List the changed files from this stat output as JSON array of strings: ${stat.stdout}"
        iter=2
        -> $files
        => s4

  s4 := local llama3-8b "Summarize the substantive changes in these files. Read each file and produce 2-4 bullet points total: ${files}"
        iter=5
        -> $summary
        => s_term

  s_nochanges   := terminal "No changes in last commit."
  s_term        := terminal "${summary}"
  s_default_err := escalate "step failed during summary generation" ctx=[$err]
}`
	p := mustParse(t, src)
	if len(p.Steps) != 7 {
		t.Errorf("steps = %d", len(p.Steps))
	}
	if got := p.Meta.Fields["manifest"].Hex; got != 0xa1b2c3d4 {
		t.Errorf("manifest hex = %#x", got)
	}
}

func TestParse_ExampleC_ErrorRecovery(t *testing.T) {
	src := `plan @v1 {
  meta {
    task: "read config file"
    max_esc: 0
    max_agents: 0
    manifest: 0xa1
    on_err: s_ask
    entry: s1
  }

  s1 := internal_tool Bash {cmd: "test -f ./config.yaml && echo found || echo missing"}
        -> $check
        => s2

  s2 := branch (contains $check.stdout "missing") => s_ask | s3

  s3 := internal_tool Read {path: "./config.yaml"}
        -> $cfg
        catch => s_ask
        => s4

  s4 := local llama3-8b "Summarize this config in 3 bullets: ${cfg}"
        iter=2
        -> $summary
        => s_term

  s_ask := ask_user "config.yaml not found. Where should I look?"
           choices=["./config.yml", "./.config/config.yaml", "skip"]
           -> $choice
           => s_choice_branch

  s_choice_branch := branch (eq $choice "skip") => s_skip | s_retry

  s_retry := internal_tool Read {path: "${choice}"}
             -> $cfg
             catch => s_skip
             => s4

  s_skip := terminal "Skipped — no config available."
  s_term := terminal "${summary}"
}`
	mustParse(t, src)
}

// --- Negative cases -----------------------------------------------------

func TestParse_ErrorMissingVersion(t *testing.T) {
	mustFail(t, `plan { meta { entry: s1 } s1 := terminal "x" }`,
		"expected '@'")
}

func TestParse_ErrorUnsupportedVersion(t *testing.T) {
	mustFail(t, `plan @v9 { meta { entry: s1 } s1 := terminal "x" }`,
		"unsupported plan version")
}

func TestParse_ErrorEmptyBody(t *testing.T) {
	mustFail(t, `plan @v1 { meta { entry: s1 } }`,
		"plan must declare at least one step")
}

func TestParse_ErrorUnknownStepKind(t *testing.T) {
	mustFail(t, `plan @v1 { meta { entry: s1 } s1 := frobnicate "x" }`,
		"unknown step kind")
}

func TestParse_ErrorBadEscape(t *testing.T) {
	mustFail(t, `plan @v1 { meta { entry: s1 } s1 := terminal "bad \q escape" }`,
		"unrecognized escape sequence")
}

func TestParse_ErrorUnterminatedString(t *testing.T) {
	mustFail(t, `plan @v1 { meta { entry: s1 } s1 := terminal "oops`,
		"unterminated string")
}

func TestParse_ErrorRendersWithSourceLine(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := frobnicate "x"
}`
	_, err := Parse(src)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line 3") {
		t.Errorf("error missing line info: %s", msg)
	}
	if !strings.Contains(msg, "frobnicate") {
		t.Errorf("error missing source line: %s", msg)
	}
	if !strings.Contains(msg, "^^^") {
		t.Errorf("error missing pointer caret: %s", msg)
	}
}
