package exec

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
)

// interpolate expands `${name[.field]+}` sequences in s against env. The
// grammar's escape rule preserves `\$` as a literal `$` inside string
// literals at lex time, so by the time we see s here, an unescaped `$`
// followed by `{` is always an interpolation. Unknown variables produce
// an error rather than an empty substitution — the semantic checker
// should have caught these before runtime, but we surface a clear error
// at the read site if it slipped through.
func interpolate(s string, env *runtimeEnv) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) || s[i+1] != '{' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ${...} interpolation in %q", s)
		}
		inner := s[i+2 : i+2+end]
		parts := strings.Split(inner, ".")
		if parts[0] == "" {
			return "", fmt.Errorf("empty interpolation '${}' in %q", s)
		}
		v, ok := env.Lookup(parts[0], parts[1:])
		if !ok {
			return "", fmt.Errorf("interpolation $%s not bound", inner)
		}
		b.WriteString(stringify(v))
		i += 2 + end + 1
	}
	return b.String(), nil
}

// stringify renders a runtime value for interpolation into a string. It
// tracks the coercions plan/eval.go uses for predicate inputs, so what
// you can match in a predicate is also what gets rendered into a prompt:
// ExecResult → its stdout, structured maps/lists → JSON, primitives →
// their text form.
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
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
	case *plan.ExecResult:
		return x.Stdout
	case plan.ExecResult:
		return x.Stdout
	}
	// Fall back to JSON for maps/slices/structs; if that fails, use %v.
	if buf, err := json.Marshal(v); err == nil {
		return string(buf)
	}
	return fmt.Sprintf("%v", v)
}

// evalLiteral resolves an AST Value to a runtime Go value. String values
// run through interpolate so `"file: ${name}"` resolves at the point of
// use. Var refs read straight from env. Lists and obj literals recurse.
func evalLiteral(v plan.Value, env *runtimeEnv) (any, error) {
	switch v.Kind {
	case plan.ValueString:
		return interpolate(v.Str, env)
	case plan.ValueNumber:
		return v.Num, nil
	case plan.ValueBool:
		return v.Bool, nil
	case plan.ValueHex:
		return v.Hex, nil
	case plan.ValueIdent:
		return v.Ident, nil
	case plan.ValueVar:
		got, ok := env.Lookup(v.Var.Name, v.Var.Fields)
		if !ok {
			return nil, fmt.Errorf("variable $%s not bound", v.Var)
		}
		return got, nil
	case plan.ValueList:
		out := make([]any, 0, len(v.List))
		for _, item := range v.List {
			r, err := evalLiteral(item, env)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		return out, nil
	case plan.ValueObj:
		return objToMap(v.Obj, env)
	}
	return nil, fmt.Errorf("unsupported literal kind %v", v.Kind)
}

// objToMap evaluates an obj_literal against env and returns it as a
// map[string]any with key order lost (JSON encoding will reorder anyway).
func objToMap(o plan.ObjLiteral, env *runtimeEnv) (map[string]any, error) {
	out := make(map[string]any, len(o.Pairs))
	for _, p := range o.Pairs {
		v, err := evalLiteral(p.Value, env)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", p.Key, err)
		}
		out[p.Key] = v
	}
	return out, nil
}

// objToJSON evaluates an obj_literal against env and marshals it to JSON.
// This is the bridge from plan AST args to the JSON-shaped tool inputs
// the tools registry deserializes.
func objToJSON(o plan.ObjLiteral, env *runtimeEnv) (json.RawMessage, error) {
	m, err := objToMap(o, env)
	if err != nil {
		return nil, err
	}
	buf, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encoding tool args: %w", err)
	}
	return buf, nil
}

// kwArgInt is a typed accessor for integer kwargs (iter, timeout_ms,
// max_iter). Returns def when the kwarg is absent.
func kwArgInt(kw plan.KWArgs, key string, def int) (int, error) {
	v, ok := kw.Get(key)
	if !ok {
		return def, nil
	}
	if v.Kind != plan.ValueNumber {
		return 0, fmt.Errorf("kwarg %s must be a number", key)
	}
	return int(v.Num), nil
}

// kwArgString is a typed accessor for string kwargs (format).
func kwArgString(kw plan.KWArgs, key string, def string, env *runtimeEnv) (string, error) {
	v, ok := kw.Get(key)
	if !ok {
		return def, nil
	}
	switch v.Kind {
	case plan.ValueString:
		return interpolate(v.Str, env)
	case plan.ValueIdent:
		return v.Ident, nil
	}
	return "", fmt.Errorf("kwarg %s must be a string or identifier", key)
}

// kwArgList returns a list-shaped kwarg's elements, evaluating each.
func kwArgList(kw plan.KWArgs, key string, env *runtimeEnv) ([]any, bool, error) {
	v, ok := kw.Get(key)
	if !ok {
		return nil, false, nil
	}
	if v.Kind != plan.ValueList {
		return nil, true, fmt.Errorf("kwarg %s must be a list", key)
	}
	out := make([]any, 0, len(v.List))
	for _, item := range v.List {
		r, err := evalLiteral(item, env)
		if err != nil {
			return nil, true, err
		}
		out = append(out, r)
	}
	return out, true, nil
}
