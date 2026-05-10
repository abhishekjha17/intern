package plan

import (
	"strings"
	"testing"
)

func mustCheck(t *testing.T, src string, ctx CheckContext) []*Error {
	t.Helper()
	p, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	errs := Check(p, ctx)
	SetSource(errs, src)
	return errs
}

func wantErr(t *testing.T, errs []*Error, category, substr string) {
	t.Helper()
	for _, e := range errs {
		if e.Category == category && (substr == "" || strings.Contains(e.Message, substr)) {
			return
		}
	}
	t.Fatalf("expected error category=%q substr=%q, got %d errors:\n%s",
		category, substr, len(errs), formatErrs(errs))
}

func wantNoErrs(t *testing.T, errs []*Error) {
	t.Helper()
	if len(errs) > 0 {
		t.Fatalf("expected no errors, got %d:\n%s", len(errs), formatErrs(errs))
	}
}

func formatErrs(errs []*Error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Format())
		b.WriteString("\n")
	}
	return b.String()
}

func TestCheck_ValidLinearPlan(t *testing.T) {
	src := `plan @v1 {
  meta { task: "ok" max_esc: 0 entry: s1 }
  s1 := internal_tool Bash {cmd: "ls"} -> $out => s2
  s2 := terminal "${out}"
}`
	wantNoErrs(t, mustCheck(t, src, CheckContext{}))
}

func TestCheck_DuplicateStepID(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := terminal "a"
  s1 := terminal "b"
}`
	errs := mustCheck(t, src, CheckContext{})
	wantErr(t, errs, "duplicate_id", "duplicate step id")
}

func TestCheck_UnresolvedEntry(t *testing.T) {
	src := `plan @v1 {
  meta { entry: nope }
  s1 := terminal "a"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "unresolved_reference", "entry references")
}

func TestCheck_UnresolvedControl(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := internal_tool Bash {cmd: "x"} -> $o => nope
  s2 := terminal "y"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "unresolved_reference", "unknown control target")
}

func TestCheck_BranchWrongTargetCount(t *testing.T) {
	// Single target on a branch.
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := branch (is_empty $x) => only
  only := terminal "x"
}`
	// Note: $x is also unbound; expect both errors.
	errs := mustCheck(t, src, CheckContext{})
	wantErr(t, errs, "missing_clause", "branch must declare two control targets")
}

func TestCheck_TerminalWithExtras(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := terminal "a" -> $r
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "invalid_clause", "terminal")
}

func TestCheck_LocalMissingOutBind(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := local llama3 "p" => s2
  s2 := terminal "x"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "missing_clause", "local step must bind")
}

func TestCheck_ParallelTooFewChildren(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := parallel [a1] => merge
  a1 := agent code-reviewer "review" -> $r
  merge := terminal "${r}"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "invalid_clause", "at least two children")
}

func TestCheck_ReservedPrefix(t *testing.T) {
	src := `plan @v1 {
  meta { entry: __secret }
  __secret := terminal "no"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "reserved_identifier", "reserved prefix")
}

func TestCheck_ReservedVarName(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := internal_tool Bash {cmd: "x"} -> $err => s2
  s2 := terminal "x"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "reserved_identifier", "reserved")
}

func TestCheck_DataflowUnboundVar(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := terminal "${ghost}"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "unresolved_reference", "never bound")
}

func TestCheck_DataflowReadBeforeBind(t *testing.T) {
	// Step s1 reads $x but $x is bound by the unreachable branch s_dead.
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := terminal "${x}"
  s_dead := internal_tool Bash {cmd: "x"} -> $x => s1
}`
	errs := mustCheck(t, src, CheckContext{})
	// Expect either unreachable_binding or unreachable_read.
	found := false
	for _, e := range errs {
		if e.Category == "unreachable_binding" || e.Category == "unreachable_read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unreachable error, got:\n%s", formatErrs(errs))
	}
}

func TestCheck_Cycle(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := internal_tool Bash {cmd: "x"} -> $a => s2
  s2 := internal_tool Bash {cmd: "y"} -> $b => s1
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "cycle_detected", "cycle")
}

func TestCheck_ManifestMatchOK(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 manifest: 0xa1b2c3d4 }
  s1 := terminal "x"
}`
	wantNoErrs(t, mustCheck(t, src, CheckContext{ExpectedManifest: "0xa1b2c3d4"}))
}

func TestCheck_ManifestMismatch(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 manifest: 0xdeadbeef }
  s1 := terminal "x"
}`
	wantErr(t, mustCheck(t, src, CheckContext{ExpectedManifest: "0xa1b2c3d4"}),
		"manifest_mismatch", "does not match")
}

func TestCheck_UnknownKWArg(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := local llama3 "p" iter=1 frobnicate=true -> $r => s2
  s2 := terminal "${r}"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "unknown_kwarg", "frobnicate")
}

func TestCheck_OnErrReadsVarBoundEarlier(t *testing.T) {
	// Real planner output: a global on_err handler escalates with a ctx
	// referencing a var bound by an earlier step. Without the on_err
	// edge in dataflow reachability, this is rejected as
	// unreachable_read even though $exists is always bound by the time
	// on_err can fire.
	src := `plan @v1 {
  meta { entry: s1 on_err: s_err }
  s1 := internal_tool Bash {cmd: "ls"} -> $exists => s2
  s2 := terminal "${exists.stdout}"
  s_err := escalate "boom" ctx=[$exists]
}`
	wantNoErrs(t, mustCheck(t, src, CheckContext{}))
}

func TestCheck_LocalAcceptsInputsKWArg(t *testing.T) {
	// `inputs` is allowed as a no-op on `local` because the planner
	// sometimes emits it by analogy with `agent` steps. The runtime
	// resolves $vars via prompt interpolation, so accepting it
	// prevents otherwise-valid plans from being rejected at check time.
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := local llama3 "load config" -> $cfg => s2
  s2 := local llama3 "summarize ${cfg}" inputs=[$cfg] -> $r => s3
  s3 := terminal "${r}"
}`
	wantNoErrs(t, mustCheck(t, src, CheckContext{}))
}

func TestCheck_DuplicateBinding(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := internal_tool Bash {cmd: "x"} -> $r => s2
  s2 := internal_tool Bash {cmd: "y"} -> $r => s3
  s3 := terminal "${r}"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "duplicate_binding", "$r")
}

func TestCheck_KnownToolsValidated(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := internal_tool Frobnicate {x: "y"} -> $r => s2
  s2 := terminal "${r}"
}`
	ctx := CheckContext{
		KnownInternalTools: []string{"Bash", "Read", "Grep", "Glob"},
	}
	wantErr(t, mustCheck(t, src, ctx), "unresolved_reference", "Frobnicate")
}

func TestCheck_KnownClientToolsValidated(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := client_tool MysteryEditor {x: "y"} -> $r => s2
  s2 := terminal "${r}"
}`
	ctx := CheckContext{
		KnownClientTools: []string{"Read", "Edit", "Write", "Bash"},
	}
	wantErr(t, mustCheck(t, src, ctx), "unresolved_reference", "MysteryEditor")
}

func TestCheck_KnownAgentProfileValidated(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  s1 := agent unknown-prof "do thing" -> $r => s2
  s2 := terminal "${r}"
}`
	ctx := CheckContext{
		KnownAgentProfiles: []string{"code-reviewer", "bug-finder"},
	}
	wantErr(t, mustCheck(t, src, ctx), "unresolved_reference", "unknown-prof")
}

func TestCheck_ConstNameConflictWithBinding(t *testing.T) {
	src := `plan @v1 {
  meta { entry: s1 }
  const x := "fixed"
  s1 := internal_tool Bash {cmd: "ls"} -> $x => s2
  s2 := terminal "${x}"
}`
	wantErr(t, mustCheck(t, src, CheckContext{}), "duplicate_binding", "conflicts")
}

func TestCheck_FullExampleCpasses(t *testing.T) {
	// Example C from planner-system-prompt.md should pass with empty context.
	src := `plan @v1 {
  meta {
    task: "read config file"
    max_esc: 0
    max_agents: 0
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

  s_retry := internal_tool Read {path: "./fallback.yaml"}
             -> $cfg2
             catch => s_skip
             => s4_retry

  s4_retry := local llama3-8b "Summarize this fallback config: ${cfg2}"
              iter=2
              -> $summary2
              => s_term_retry

  s_skip := terminal "Skipped — no config available."
  s_term := terminal "${summary}"
  s_term_retry := terminal "${summary2}"
}`
	errs := mustCheck(t, src, CheckContext{})
	if len(errs) > 0 {
		t.Fatalf("expected example C to type-check; got:\n%s", formatErrs(errs))
	}
}
