package plan

import (
	"errors"
	"testing"
)

// parsePred wraps a predicate-only source (e.g. `(regex_match $x "y")`) into a
// minimal valid plan and returns the parsed predicate.
func parsePred(t *testing.T, predSrc string) Predicate {
	t.Helper()
	src := "plan @v1 {\n  meta { entry: s1 }\n  s1 := branch " + predSrc + " => a | b\n  a := terminal \"x\"\n  b := terminal \"y\"\n}"
	p, err := Parse(src)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return p.Steps[0].(*BranchStep).Pred
}

func TestEval_RegexMatch(t *testing.T) {
	pred := parsePred(t, `(regex_match $msg "ERROR")`)
	b := MapBindings{"msg": "build failed: ERROR at line 5"}
	got, err := Eval(pred, b, nil)
	if err != nil || !got {
		t.Errorf("got=%v err=%v, want true", got, err)
	}

	b2 := MapBindings{"msg": "all good"}
	got, err = Eval(pred, b2, nil)
	if err != nil || got {
		t.Errorf("got=%v err=%v, want false", got, err)
	}
}

func TestEval_ExitCode(t *testing.T) {
	pred := parsePred(t, `(exit_code_eq $r 0)`)
	b := MapBindings{"r": &ExecResult{Stdout: "", Exit: 0}}
	got, _ := Eval(pred, b, nil)
	if !got {
		t.Errorf("expected true for exit=0")
	}

	b["r"] = &ExecResult{Exit: 1}
	got, _ = Eval(pred, b, nil)
	if got {
		t.Errorf("expected false for exit=1")
	}
}

func TestEval_Contains(t *testing.T) {
	pred := parsePred(t, `(contains $stdout "missing")`)
	b := MapBindings{"stdout": "file is missing"}
	got, _ := Eval(pred, b, nil)
	if !got {
		t.Error("expected true for substring match")
	}
}

func TestEval_LenAndEmpty(t *testing.T) {
	pred := parsePred(t, `(len_gt $items 2)`)
	b := MapBindings{"items": []any{1, 2, 3, 4}}
	got, _ := Eval(pred, b, nil)
	if !got {
		t.Error("expected true for len 4 > 2")
	}

	predEmpty := parsePred(t, `(is_empty $stuff)`)
	b2 := MapBindings{"stuff": ""}
	got, _ = Eval(predEmpty, b2, nil)
	if !got {
		t.Error("expected true for empty string")
	}
}

func TestEval_EqAndNe(t *testing.T) {
	pred := parsePred(t, `(eq $choice "yes")`)
	b := MapBindings{"choice": "yes"}
	got, _ := Eval(pred, b, nil)
	if !got {
		t.Error("expected eq true")
	}

	predNe := parsePred(t, `(ne $choice "no")`)
	got, _ = Eval(predNe, b, nil)
	if !got {
		t.Error("expected ne true")
	}
}

func TestEval_Combinators(t *testing.T) {
	pred := parsePred(t, `(and (regex_match $a "go") (or (eq $b 1) (eq $b 2)))`)
	b := MapBindings{"a": "go-test", "b": 2.0}
	got, err := Eval(pred, b, nil)
	if err != nil || !got {
		t.Errorf("got=%v err=%v", got, err)
	}

	notP := parsePred(t, `(not (eq $b 99))`)
	got, _ = Eval(notP, b, nil)
	if !got {
		t.Error("expected not eq true")
	}
}

func TestEval_FieldChain(t *testing.T) {
	pred := parsePred(t, `(eq $r.exit 0)`)
	b := MapBindings{"r": &ExecResult{Exit: 0}}
	got, err := Eval(pred, b, nil)
	if err != nil || !got {
		t.Errorf("got=%v err=%v", got, err)
	}
}

func TestEval_JSONPath(t *testing.T) {
	pred := parsePred(t, `(json_path $data "$.code" == 200)`)
	b := MapBindings{"data": map[string]any{"code": 200.0, "msg": "ok"}}
	got, err := Eval(pred, b, nil)
	if err != nil || !got {
		t.Errorf("got=%v err=%v", got, err)
	}

	predGt := parsePred(t, `(json_path $data "$.code" > 100)`)
	got, _ = Eval(predGt, b, nil)
	if !got {
		t.Error("expected 200 > 100 to be true")
	}
}

func TestEval_Classify_Hook(t *testing.T) {
	called := false
	hook := func(input, model string, labels []string) (string, error) {
		called = true
		if model != "llama3-8b" {
			t.Errorf("model=%q", model)
		}
		if len(labels) != 2 {
			t.Errorf("labels=%v", labels)
		}
		return "ok", nil
	}
	pred := parsePred(t, `(classify $val llama3-8b labels=["ok", "bad"] expected="ok")`)
	b := MapBindings{"val": "looks fine"}
	got, err := Eval(pred, b, hook)
	if err != nil || !got {
		t.Errorf("got=%v err=%v", got, err)
	}
	if !called {
		t.Error("classifier not invoked")
	}
}

func TestEval_Classify_NoHook(t *testing.T) {
	pred := parsePred(t, `(classify $val llama3-8b labels=["ok"] expected="ok")`)
	_, err := Eval(pred, MapBindings{"val": "x"}, nil)
	if err == nil {
		t.Fatal("expected error for missing classifier")
	}
}

func TestEval_Classify_HookError(t *testing.T) {
	hook := func(input, model string, labels []string) (string, error) {
		return "", errors.New("ollama timeout")
	}
	pred := parsePred(t, `(classify $val llama3-8b labels=["ok"] expected="ok")`)
	_, err := Eval(pred, MapBindings{"val": "x"}, hook)
	if err == nil {
		t.Fatal("expected error from hook propagated")
	}
}

func TestEval_VarNotBound(t *testing.T) {
	pred := parsePred(t, `(regex_match $missing "x")`)
	_, err := Eval(pred, MapBindings{}, nil)
	if err == nil {
		t.Fatal("expected error for unbound var")
	}
}
