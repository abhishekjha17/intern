// Package plan implements the plan@v1 grammar: lexer, parser, semantic
// checker, and AST types. See docs/orchestrator/plan-grammar.md for the
// language reference.
package plan

import (
	"fmt"
	"strings"
)

// Span identifies a byte range in source. Line and Column are 1-indexed and
// computed lazily from the byte offset by the lexer.
type Span struct {
	Start, End int // byte offsets, half-open [Start, End)
	Line, Col  int // 1-indexed, computed from Start
}

func (s Span) String() string { return fmt.Sprintf("%d:%d", s.Line, s.Col) }

// ---- Plan ----------------------------------------------------------------

// Plan is the root AST node. A parsed plan is guaranteed to be syntactically
// well-formed but not yet semantically checked.
type Plan struct {
	Version string // "v1"
	Meta    Meta
	Consts  []ConstDecl
	Steps   []Step
	Span    Span
}

// Meta holds the meta block's fields. Validation of required keys, value
// types, and clamps happens in the semantic checker.
type Meta struct {
	Fields map[string]MetaValue
	Span   Span
}

// MetaValue is one entry in the meta block. Exactly one of the typed fields
// is populated per the discriminator Kind.
type MetaValue struct {
	Kind  MetaKind
	Str   string
	Num   float64
	Hex   uint64
	Bool  bool
	Ident string
	Span  Span
}

type MetaKind uint8

const (
	MetaString MetaKind = iota
	MetaNumber
	MetaHex
	MetaBool
	MetaIdent
)

// ConstDecl binds a name to a literal at plan-load time.
type ConstDecl struct {
	Name  string
	Value Value
	Span  Span
}

// ---- Steps ---------------------------------------------------------------

// Step is the common interface for every step kind. The discriminator is the
// concrete Go type. Every step has an ID, source span, and the optional
// out-bind, control, and catch clauses parsed off the trailing portion of
// the step line.
type Step interface {
	StepID() string
	StepSpan() Span
	StepKind() string

	// OutBind returns the variable bound by this step's "-> $var" clause,
	// or empty string if absent.
	OutBind() string

	// Control returns the step IDs targeted by this step's "=> a [| b]" clause.
	// Empty for terminal/escalate-no-resume; one ID for linear/parallel/agent/
	// local/internal_tool/client_tool/ask_user; two IDs for branch.
	Control() []string

	// Catch returns the step ID for this step's "catch => err_step" clause,
	// or empty string if absent.
	Catch() string
}

// stepBase holds the fields every step parses: ID, source span, out-bind,
// control targets, and catch clause. Concrete step kinds embed this.
type stepBase struct {
	id       string
	span     Span
	outBind  string
	control  []string
	catchTo  string
}

func (b *stepBase) StepID() string   { return b.id }
func (b *stepBase) StepSpan() Span   { return b.span }
func (b *stepBase) OutBind() string  { return b.outBind }
func (b *stepBase) Control() []string { return b.control }
func (b *stepBase) Catch() string    { return b.catchTo }

// LocalStep — `local <model_id> "<prompt>" [kw_args]`
type LocalStep struct {
	stepBase
	ModelID string
	Prompt  string
	KWArgs  KWArgs
}

func (s *LocalStep) StepKind() string { return "local" }

// InternalToolStep — `internal_tool <tool_ref> {<args>}`
type InternalToolStep struct {
	stepBase
	Tool ToolRef
	Args ObjLiteral
}

func (s *InternalToolStep) StepKind() string { return "internal_tool" }

// ClientToolStep — `client_tool <tool_ref> {<args>}`
type ClientToolStep struct {
	stepBase
	Tool ToolRef
	Args ObjLiteral
}

func (s *ClientToolStep) StepKind() string { return "client_tool" }

// BranchStep — `branch <predicate>` (control has exactly 2 targets)
type BranchStep struct {
	stepBase
	Pred Predicate
}

func (s *BranchStep) StepKind() string { return "branch" }

// ParallelStep — `parallel [id, id, ...]` (control has 1 merge target)
type ParallelStep struct {
	stepBase
	Children []string
}

func (s *ParallelStep) StepKind() string { return "parallel" }

// AgentStep — `agent <profile> "<prompt>" [kw_args]`
type AgentStep struct {
	stepBase
	Profile string
	Prompt  string
	KWArgs  KWArgs
}

func (s *AgentStep) StepKind() string { return "agent" }

// AskUserStep — `ask_user "<question>" [kw_args]`
type AskUserStep struct {
	stepBase
	Question string
	KWArgs   KWArgs
}

func (s *AskUserStep) StepKind() string { return "ask_user" }

// TerminalStep — `terminal "<message_template>"` (no out-bind, no control, no catch)
type TerminalStep struct {
	stepBase
	Message string
}

func (s *TerminalStep) StepKind() string { return "terminal" }

// EscalateStep — `escalate "<reason>" [kw_args]` (control optional, 0 or 1 target)
type EscalateStep struct {
	stepBase
	Reason string
	KWArgs KWArgs
}

func (s *EscalateStep) StepKind() string { return "escalate" }

// ---- Tool refs and arg objects ------------------------------------------

// ToolRef is a tool name optionally suffixed with a surface (`:client` or
// `:internal`). When Surface is empty the orchestrator applies the default
// surface for the tool kind (read-only → internal, mutating → client).
type ToolRef struct {
	Name    string
	Surface string // "client" | "internal" | ""
	Span    Span
}

func (t ToolRef) String() string {
	if t.Surface == "" {
		return t.Name
	}
	return t.Name + ":" + t.Surface
}

// ---- Keyword args -------------------------------------------------------

// KWArgs is an ordered collection of keyword arguments. Order is preserved
// for diagnostics; lookup is by name (last write wins, but the parser rejects
// duplicate keys per step before reaching the AST).
type KWArgs struct {
	Pairs []KWPair
}

type KWPair struct {
	Key   string
	Value Value
	Span  Span
}

// Get returns the first value bound to key, and whether it was present.
func (k KWArgs) Get(key string) (Value, bool) {
	for _, p := range k.Pairs {
		if p.Key == key {
			return p.Value, true
		}
	}
	return Value{}, false
}

// ---- Predicates ---------------------------------------------------------

// Predicate is the s-expression predicate language. Combinators (And, Or,
// Not) wrap operand predicates; atoms carry an op identifier and a list of
// pred-args.
type Predicate interface {
	predicate()
	PredSpan() Span
}

// PredAtom — a single predicate operation, e.g. (regex_match $v "p")
type PredAtom struct {
	Op   string
	Args []PredArg
	Span Span
}

func (PredAtom) predicate()     {}
func (p PredAtom) PredSpan() Span { return p.Span }

// PredAnd — (and <pred>+)
type PredAnd struct {
	Operands []Predicate
	Span     Span
}

func (PredAnd) predicate()    {}
func (p PredAnd) PredSpan() Span { return p.Span }

// PredOr — (or <pred>+)
type PredOr struct {
	Operands []Predicate
	Span     Span
}

func (PredOr) predicate()    {}
func (p PredOr) PredSpan() Span { return p.Span }

// PredNot — (not <pred>)
type PredNot struct {
	Operand Predicate
	Span    Span
}

func (PredNot) predicate()    {}
func (p PredNot) PredSpan() Span { return p.Span }

// PredArg is a predicate operand: variable reference, literal, identifier,
// or list. (Object literals are not valid pred args in v1.)
type PredArg struct {
	Kind PredArgKind
	Var  *VarRef
	Str  string
	Num  float64
	Bool bool
	Ident string
	List []Value
	KW   *KWPair // for keyword-style args inside predicates (labels=, expected=, etc.)
	Span Span
}

type PredArgKind uint8

const (
	PredArgVar PredArgKind = iota
	PredArgString
	PredArgNumber
	PredArgBool
	PredArgIdent
	PredArgList
	PredArgKWPair
)

// ---- Values and literals ------------------------------------------------

// Value is a literal value usable in kw_args, obj_literal, list_literal, or
// const decls. Like MetaValue, exactly one typed field is populated per Kind.
//
// ValueIdent carries an extended identifier (tool ref, model id, label, etc.)
// produced from a contiguous run of letter/digit/dash/colon/dot bytes. The
// semantic checker resolves what kind of reference it actually is based on
// context (the kwarg name, the position in a predicate, etc.).
type Value struct {
	Kind  ValueKind
	Str   string     // ValueString — may contain ${...} interpolations
	Num   float64    // ValueNumber
	Bool  bool       // ValueBool
	Hex   uint64     // ValueHex
	List  []Value    // ValueList
	Obj   ObjLiteral // ValueObj
	Var   *VarRef    // ValueVar
	Ident string     // ValueIdent
	Span  Span
}

type ValueKind uint8

const (
	ValueString ValueKind = iota
	ValueNumber
	ValueBool
	ValueHex
	ValueList
	ValueObj
	ValueVar
	ValueIdent
)

// VarRef is `$name[.field]+` — a base variable name with an optional chain
// of dot-separated field accesses. The lexer accepts arbitrary chains; the
// semantic checker validates that the field path is meaningful given the
// type of the bound variable.
type VarRef struct {
	Name   string
	Fields []string
	Span   Span
}

func (v VarRef) String() string {
	if len(v.Fields) == 0 {
		return "$" + v.Name
	}
	return "$" + v.Name + "." + strings.Join(v.Fields, ".")
}

// ObjLiteral is a `{key: value, ...}` map literal. Key order is preserved
// for diagnostics and stable serialization.
type ObjLiteral struct {
	Pairs []ObjPair
	Span  Span
}

type ObjPair struct {
	Key   string
	Value Value
	Span  Span
}

// Get returns the first value bound to key, and whether it was present.
func (o ObjLiteral) Get(key string) (Value, bool) {
	for _, p := range o.Pairs {
		if p.Key == key {
			return p.Value, true
		}
	}
	return Value{}, false
}

// ---- Errors -------------------------------------------------------------

// Error is the compiler-style diagnostic emitted by both the parser and the
// semantic checker. It is the load-bearing UX of the system: Opus self-
// corrects against these on the single retry budget.
type Error struct {
	Category string // "syntax", "arity_mismatch", "unresolved_reference", ...
	Message  string
	Hint     string // optional suggested fix
	Span     Span
	Source   string // the full source string, used to render the pointer line
}

// Format renders the error in compiler-diagnostic shape:
//
//	plan@v1 error at line L col C: <category>: <message>
//	  L | <source line>
//	    |     ^^^ <pointer>
//	  hint: <suggested fix>
func (e *Error) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "plan@v1 error at line %d col %d: %s: %s\n",
		e.Span.Line, e.Span.Col, e.Category, e.Message)

	line := lineAt(e.Source, e.Span.Line)
	if line != "" {
		fmt.Fprintf(&b, "  %d | %s\n", e.Span.Line, line)
		// Pointer width = max(1, span length)
		n := e.Span.End - e.Span.Start
		if n < 1 {
			n = 1
		}
		fmt.Fprintf(&b, "    | %s%s\n", strings.Repeat(" ", e.Span.Col-1), strings.Repeat("^", n))
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, "  hint: %s\n", e.Hint)
	}
	return b.String()
}

func (e *Error) Error() string { return e.Format() }

// lineAt returns the 1-indexed line from src, or "" if line is out of range.
func lineAt(src string, line int) string {
	if line <= 0 {
		return ""
	}
	cur := 1
	start := 0
	for i := 0; i < len(src); i++ {
		if cur == line {
			end := i
			for end < len(src) && src[end] != '\n' {
				end++
			}
			return src[start:end]
		}
		if src[i] == '\n' {
			cur++
			start = i + 1
		}
	}
	if cur == line {
		return src[start:]
	}
	return ""
}
