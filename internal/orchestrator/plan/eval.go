package plan

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// Bindings is the runtime variable environment the predicate evaluator
// queries. Concrete implementations are provided by the orchestrator.
type Bindings interface {
	// Lookup returns the value at $name (with optional dotted field chain),
	// and whether it exists. Implementations should return concrete Go
	// types: string, []byte, int64, float64, bool, []any, map[string]any,
	// or *ExecResult for shell exit info.
	Lookup(name string, fields []string) (any, bool)
}

// ExecResult is the canonical shape of a shell-command result. The Bash
// internal-tool implementation returns this struct as its bound value, and
// predicates like exit_code_eq operate on it.
type ExecResult struct {
	Stdout string
	Stderr string
	Exit   int
}

// Classifier is the hook predicate evaluation calls when it encounters
// `(classify $var <model_id> labels=[...] expected=...)`. Implementations
// run the named local model on the input and return one of the labels.
//
// The evaluator passes in: the value coerced to a string, the model id,
// and the closed label set. The implementation must return a string that
// is in `labels` (or return an error).
type Classifier func(input, modelID string, labels []string) (string, error)

// Eval evaluates a predicate against the bindings using the supplied
// classifier hook. A nil classifier causes any classify() predicate to
// fail with an error.
func Eval(p Predicate, b Bindings, classify Classifier) (bool, error) {
	switch v := p.(type) {
	case PredAnd:
		for _, op := range v.Operands {
			r, err := Eval(op, b, classify)
			if err != nil {
				return false, err
			}
			if !r {
				return false, nil
			}
		}
		return true, nil
	case PredOr:
		for _, op := range v.Operands {
			r, err := Eval(op, b, classify)
			if err != nil {
				return false, err
			}
			if r {
				return true, nil
			}
		}
		return false, nil
	case PredNot:
		r, err := Eval(v.Operand, b, classify)
		if err != nil {
			return false, err
		}
		return !r, nil
	case PredAtom:
		return evalAtom(v, b, classify)
	}
	return false, fmt.Errorf("unhandled predicate type %T", p)
}

// evalAtom dispatches on the atom's op.
func evalAtom(a PredAtom, b Bindings, classify Classifier) (bool, error) {
	switch a.Op {
	case "exit_code_eq":
		return cmpExitCode(a, b, true)
	case "exit_code_ne":
		return cmpExitCode(a, b, false)
	case "regex_match":
		return cmpRegex(a, b, true)
	case "not_regex_match":
		return cmpRegex(a, b, false)
	case "contains":
		return cmpContains(a, b)
	case "len_gt":
		return cmpLen(a, b, ">")
	case "len_lt":
		return cmpLen(a, b, "<")
	case "len_eq":
		return cmpLen(a, b, "==")
	case "is_empty":
		return cmpIsEmpty(a, b, true)
	case "is_nonempty":
		return cmpIsEmpty(a, b, false)
	case "eq":
		return cmpEq(a, b, true)
	case "ne":
		return cmpEq(a, b, false)
	case "json_path":
		return cmpJSONPath(a, b)
	case "classify":
		return cmpClassify(a, b, classify)
	}
	return false, fmt.Errorf("unknown predicate op %q", a.Op)
}

// ---- Per-op helpers ----------------------------------------------------

func cmpExitCode(a PredAtom, b Bindings, eq bool) (bool, error) {
	if len(a.Args) != 2 {
		return false, fmt.Errorf("exit_code_* expects (var, int), got %d args", len(a.Args))
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("exit_code_*: var $%s not bound", a.Args[0].Var.Name)
	}
	want, err := argInt(a.Args[1])
	if err != nil {
		return false, err
	}
	got, err := coerceExit(v)
	if err != nil {
		return false, fmt.Errorf("exit_code_*: %w", err)
	}
	if eq {
		return got == want, nil
	}
	return got != want, nil
}

func cmpRegex(a PredAtom, b Bindings, want bool) (bool, error) {
	if len(a.Args) != 2 {
		return false, fmt.Errorf("regex_match expects (var, pattern), got %d args", len(a.Args))
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("regex_match: var not bound")
	}
	pat, err := argString(a.Args[1])
	if err != nil {
		return false, err
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return false, fmt.Errorf("invalid regex %q: %w", pat, err)
	}
	s := coerceString(v)
	matched := re.MatchString(s)
	if want {
		return matched, nil
	}
	return !matched, nil
}

func cmpContains(a PredAtom, b Bindings) (bool, error) {
	if len(a.Args) != 2 {
		return false, fmt.Errorf("contains expects (var, needle), got %d args", len(a.Args))
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("contains: var not bound")
	}
	needle, err := argString(a.Args[1])
	if err != nil {
		return false, err
	}
	return strings.Contains(coerceString(v), needle), nil
}

func cmpLen(a PredAtom, b Bindings, op string) (bool, error) {
	if len(a.Args) != 2 {
		return false, fmt.Errorf("len_* expects (var, int), got %d args", len(a.Args))
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("len_*: var not bound")
	}
	want, err := argInt(a.Args[1])
	if err != nil {
		return false, err
	}
	got, err := coerceLen(v)
	if err != nil {
		return false, fmt.Errorf("len_*: %w", err)
	}
	switch op {
	case ">":
		return got > want, nil
	case "<":
		return got < want, nil
	case "==":
		return got == want, nil
	}
	return false, fmt.Errorf("unknown len op %q", op)
}

func cmpIsEmpty(a PredAtom, b Bindings, want bool) (bool, error) {
	if len(a.Args) != 1 {
		return false, fmt.Errorf("is_empty/is_nonempty expects (var)")
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("is_empty/is_nonempty: var not bound")
	}
	got, err := coerceLen(v)
	if err != nil {
		return false, err
	}
	empty := got == 0
	if want {
		return empty, nil
	}
	return !empty, nil
}

func cmpEq(a PredAtom, b Bindings, want bool) (bool, error) {
	if len(a.Args) != 2 {
		return false, fmt.Errorf("eq/ne expects (var, value)")
	}
	lhs, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("eq/ne: var not bound")
	}
	rhs, err := argRuntimeValue(a.Args[1])
	if err != nil {
		return false, err
	}
	equal := runtimeEqual(lhs, rhs)
	if want {
		return equal, nil
	}
	return !equal, nil
}

func cmpJSONPath(a PredAtom, b Bindings) (bool, error) {
	if len(a.Args) != 4 {
		return false, fmt.Errorf("json_path expects (var, path, cmp_op, value); got %d args", len(a.Args))
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("json_path: var not bound")
	}
	path, err := argString(a.Args[1])
	if err != nil {
		return false, err
	}
	op, err := argString(a.Args[2])
	if err != nil {
		// Op may have come through as PredArgIdent rather than string.
		if a.Args[2].Kind == PredArgIdent {
			op = a.Args[2].Ident
		} else {
			return false, fmt.Errorf("json_path: cmp_op must be one of == != < > <= >=")
		}
	}
	rhs, err := argRuntimeValue(a.Args[3])
	if err != nil {
		return false, err
	}
	got, ok := jsonPathLookup(v, path)
	if !ok {
		return false, nil // missing path → predicate is false, not an error
	}
	return runtimeCompare(got, op, rhs)
}

func cmpClassify(a PredAtom, b Bindings, classify Classifier) (bool, error) {
	if classify == nil {
		return false, fmt.Errorf("classify predicate used but no classifier hook supplied")
	}
	// Positional: var, model_id. KW: labels=[...], expected="...".
	if len(a.Args) < 2 {
		return false, fmt.Errorf("classify expects (var, model_id, labels=[...], expected=\"...\")")
	}
	v, ok := lookupArg(a.Args[0], b)
	if !ok {
		return false, fmt.Errorf("classify: var not bound")
	}
	modelID := ""
	switch a.Args[1].Kind {
	case PredArgIdent:
		modelID = a.Args[1].Ident
	case PredArgString:
		modelID = a.Args[1].Str
	default:
		return false, fmt.Errorf("classify: second arg must be a model id (identifier)")
	}
	var labels []string
	expected := ""
	for _, ar := range a.Args[2:] {
		if ar.Kind != PredArgKWPair || ar.KW == nil {
			return false, fmt.Errorf("classify: extra positional arg not allowed (use labels=, expected=)")
		}
		switch ar.KW.Key {
		case "labels":
			if ar.KW.Value.Kind != ValueList {
				return false, fmt.Errorf("classify: labels must be a list of strings")
			}
			for _, item := range ar.KW.Value.List {
				if item.Kind != ValueString {
					return false, fmt.Errorf("classify: labels entries must be strings")
				}
				labels = append(labels, item.Str)
			}
		case "expected":
			if ar.KW.Value.Kind != ValueString {
				return false, fmt.Errorf("classify: expected must be a string")
			}
			expected = ar.KW.Value.Str
		default:
			return false, fmt.Errorf("classify: unknown kwarg %q", ar.KW.Key)
		}
	}
	if len(labels) == 0 || expected == "" {
		return false, fmt.Errorf("classify requires labels=[...] and expected=\"...\"")
	}
	got, err := classify(coerceString(v), modelID, labels)
	if err != nil {
		return false, fmt.Errorf("classify: %w", err)
	}
	return got == expected, nil
}

// ---- Argument extraction -----------------------------------------------

func lookupArg(a PredArg, b Bindings) (any, bool) {
	if a.Kind != PredArgVar || a.Var == nil {
		return nil, false
	}
	return b.Lookup(a.Var.Name, a.Var.Fields)
}

func argInt(a PredArg) (int64, error) {
	if a.Kind == PredArgNumber {
		return int64(a.Num), nil
	}
	return 0, fmt.Errorf("expected integer arg, got %v", a.Kind)
}

func argString(a PredArg) (string, error) {
	if a.Kind == PredArgString {
		return a.Str, nil
	}
	return "", fmt.Errorf("expected string arg, got %v", a.Kind)
}

func argRuntimeValue(a PredArg) (any, error) {
	switch a.Kind {
	case PredArgString:
		return a.Str, nil
	case PredArgNumber:
		// Use int64 when integral, else float64 — matches how json.Unmarshal
		// returns numbers (always float64). We normalize at compare time.
		return a.Num, nil
	case PredArgBool:
		return a.Bool, nil
	case PredArgIdent:
		return a.Ident, nil
	case PredArgList:
		out := make([]any, 0, len(a.List))
		for _, item := range a.List {
			rv, err := valueToRuntime(item)
			if err != nil {
				return nil, err
			}
			out = append(out, rv)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported value arg %v", a.Kind)
}

func valueToRuntime(v Value) (any, error) {
	switch v.Kind {
	case ValueString:
		return v.Str, nil
	case ValueNumber:
		return v.Num, nil
	case ValueBool:
		return v.Bool, nil
	case ValueIdent:
		return v.Ident, nil
	case ValueList:
		out := make([]any, 0, len(v.List))
		for _, e := range v.List {
			r, err := valueToRuntime(e)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported literal value kind %v", v.Kind)
}

// ---- Coercions ---------------------------------------------------------

func coerceString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case *ExecResult:
		return x.Stdout
	case ExecResult:
		return x.Stdout
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprintf("%v", v)
}

func coerceExit(v any) (int64, error) {
	switch x := v.(type) {
	case *ExecResult:
		return int64(x.Exit), nil
	case ExecResult:
		return int64(x.Exit), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	}
	return 0, fmt.Errorf("value is not an exit code (got %T)", v)
}

func coerceLen(v any) (int64, error) {
	switch x := v.(type) {
	case string:
		return int64(len(x)), nil
	case []byte:
		return int64(len(x)), nil
	case *ExecResult:
		return int64(len(x.Stdout)), nil
	case ExecResult:
		return int64(len(x.Stdout)), nil
	case []any:
		return int64(len(x)), nil
	case map[string]any:
		return int64(len(x)), nil
	}
	// Fallback via reflect for slices/maps of concrete types.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
		return int64(rv.Len()), nil
	}
	return 0, fmt.Errorf("len_*: cannot take length of %T", v)
}

// ---- Comparison --------------------------------------------------------

func runtimeEqual(a, b any) bool {
	// Fast-path direct comparison.
	if reflect.DeepEqual(a, b) {
		return true
	}
	// Numeric promotion: int → float64.
	an, aok := numeric(a)
	bn, bok := numeric(b)
	if aok && bok {
		return an == bn
	}
	// String comparison after coercion (e.g. ExecResult.Stdout vs string).
	return coerceString(a) == coerceString(b)
}

func runtimeCompare(a any, op string, b any) (bool, error) {
	if op == "==" {
		return runtimeEqual(a, b), nil
	}
	if op == "!=" {
		return !runtimeEqual(a, b), nil
	}
	an, aok := numeric(a)
	bn, bok := numeric(b)
	if !aok || !bok {
		return false, fmt.Errorf("ordering compare requires numeric operands (got %T %s %T)", a, op, b)
	}
	switch op {
	case "<":
		return an < bn, nil
	case ">":
		return an > bn, nil
	case "<=":
		return an <= bn, nil
	case ">=":
		return an >= bn, nil
	}
	return false, fmt.Errorf("unknown comparison op %q", op)
}

func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// ---- JSON path ---------------------------------------------------------

// jsonPathLookup applies a minimal JSONPath subset to v: $, $.key, $.k1.k2,
// $[0], and combinations thereof. Returns the value at the path and whether
// it exists.
//
// v may be a Go map[string]any / []any (decoded JSON), a string containing
// raw JSON (decoded on demand), or a []byte. Other types yield (nil, false).
func jsonPathLookup(v any, path string) (any, bool) {
	root := decodeJSON(v)
	if root == nil {
		return nil, false
	}
	if path == "" || path == "$" {
		return root, true
	}
	if !strings.HasPrefix(path, "$") {
		return nil, false
	}
	cur := root
	rest := path[1:]
	for len(rest) > 0 {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			i := 0
			for i < len(rest) && rest[i] != '.' && rest[i] != '[' {
				i++
			}
			if i == 0 {
				return nil, false
			}
			key := rest[:i]
			rest = rest[i:]
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = m[key]
			if !ok {
				return nil, false
			}
		case '[':
			rest = rest[1:]
			i := strings.IndexByte(rest, ']')
			if i < 0 {
				return nil, false
			}
			idx, err := strconv.Atoi(rest[:i])
			if err != nil {
				return nil, false
			}
			rest = rest[i+1:]
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func decodeJSON(v any) any {
	switch x := v.(type) {
	case map[string]any, []any:
		return x
	case string:
		var out any
		if err := json.Unmarshal([]byte(x), &out); err == nil {
			return out
		}
	case []byte:
		var out any
		if err := json.Unmarshal(x, &out); err == nil {
			return out
		}
	}
	return nil
}

// ---- Simple bindings used by the orchestrator and tests ----------------

// MapBindings is a basic Bindings implementation backed by a map[string]any.
// Field paths walk struct fields (via reflect) for known runtime types
// (ExecResult fields), and map keys for map[string]any values.
type MapBindings map[string]any

func (m MapBindings) Lookup(name string, fields []string) (any, bool) {
	v, ok := m[name]
	if !ok {
		return nil, false
	}
	for _, f := range fields {
		v, ok = mapField(v, f)
		if !ok {
			return nil, false
		}
	}
	return v, true
}

// mapField walks a single field/key access on v.
func mapField(v any, f string) (any, bool) {
	switch x := v.(type) {
	case *ExecResult:
		switch f {
		case "stdout":
			return x.Stdout, true
		case "stderr":
			return x.Stderr, true
		case "exit":
			return int64(x.Exit), true
		}
		return nil, false
	case ExecResult:
		switch f {
		case "stdout":
			return x.Stdout, true
		case "stderr":
			return x.Stderr, true
		case "exit":
			return int64(x.Exit), true
		}
		return nil, false
	case map[string]any:
		r, ok := x[f]
		return r, ok
	case []any:
		// Allow $list.0 syntax as an alternative to $list[0].
		i, err := strconv.Atoi(f)
		if err != nil || i < 0 || i >= len(x) {
			return nil, false
		}
		return x[i], true
	}
	return nil, false
}
