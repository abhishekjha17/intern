package plan

import (
	"fmt"
)

// Parse parses a plan@v1 source string into a Plan AST. The returned error,
// if any, is a *Error with a compiler-style diagnostic. Semantic validation
// (DAG check, dataflow, kwarg allowlists, ...) is done by Check, not here.
func Parse(src string) (*Plan, error) {
	p := &parser{
		src: src,
		lx:  NewLexer(src),
	}
	return p.parsePlan()
}

// parser carries lexer state and uses panic/recover to thread errors out of
// recursive-descent calls without manual error checks at every site.
type parser struct {
	src string
	lx  *Lexer
}

// parseError is the panic value used internally; recovered at parsePlan top.
type parseError struct{ err *Error }

// ---- Top-level ---------------------------------------------------------

func (p *parser) parsePlan() (plan *Plan, err error) {
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseError); ok {
				err = pe.err
				return
			}
			panic(r)
		}
	}()

	start := p.lx.Peek().Span
	p.expectIdent("plan", "expected program to start with 'plan'")
	p.expect(TokAt, "expected '@' after 'plan'")
	verTok := p.expect(TokIdent, "expected version after '@', e.g. 'v1'")
	if verTok.Str != "v1" {
		p.errAt(verTok.Span, "syntax", fmt.Sprintf("unsupported plan version %q", verTok.Str),
			"only 'v1' is supported in this build")
	}

	p.expect(TokLBrace, "expected '{' after 'plan @v1'")

	meta := p.parseMeta()

	plan = &Plan{
		Version: verTok.Str,
		Meta:    meta,
		Span:    start,
	}

	for {
		t := p.lx.Peek()
		if t.Kind == TokRBrace || t.Kind == TokEOF {
			break
		}
		if t.Kind == TokIdent && t.Str == "const" {
			plan.Consts = append(plan.Consts, p.parseConstDecl())
			continue
		}
		plan.Steps = append(plan.Steps, p.parseStep())
	}

	end := p.expect(TokRBrace, "expected '}' to close plan body")
	plan.Span.End = end.Span.End

	if t := p.lx.Next(); t.Kind != TokEOF {
		p.errAt(t.Span, "syntax", "unexpected content after closing '}'",
			"the plan body should be the entire program")
	}

	if len(plan.Steps) == 0 {
		p.errAt(plan.Span, "syntax", "plan must declare at least one step",
			"add a step like `s1 := terminal \"...\"` inside the plan body")
	}

	return plan, nil
}

// ---- Meta block --------------------------------------------------------

func (p *parser) parseMeta() Meta {
	startTok := p.expectIdent("meta", "expected 'meta' block as the first item in the plan body")
	p.expect(TokLBrace, "expected '{' after 'meta'")

	m := Meta{
		Fields: map[string]MetaValue{},
		Span:   startTok.Span,
	}
	for {
		t := p.lx.Peek()
		if t.Kind == TokRBrace {
			break
		}
		if t.Kind != TokIdent {
			p.errAt(t.Span, "syntax", "expected meta field name (identifier)",
				"meta fields look like `task: \"...\"` or `max_esc: 1`")
		}
		key := p.lx.Next()
		p.expect(TokColon, fmt.Sprintf("expected ':' after meta field %q", key.Str))
		mv := p.parseMetaValue()
		if _, dup := m.Fields[key.Str]; dup {
			p.errAt(key.Span, "syntax", fmt.Sprintf("duplicate meta field %q", key.Str), "")
		}
		m.Fields[key.Str] = mv
	}
	end := p.expect(TokRBrace, "expected '}' to close meta block")
	m.Span.End = end.Span.End
	return m
}

func (p *parser) parseMetaValue() MetaValue {
	t := p.lx.Peek()
	switch t.Kind {
	case TokString:
		p.lx.Next()
		return MetaValue{Kind: MetaString, Str: t.Str, Span: t.Span}
	case TokNumber:
		p.lx.Next()
		return MetaValue{Kind: MetaNumber, Num: t.Num, Span: t.Span}
	case TokDash:
		// Negative number literal.
		dash := p.lx.Next()
		num := p.expect(TokNumber, "expected number after '-'")
		sp := dash.Span
		sp.End = num.Span.End
		return MetaValue{Kind: MetaNumber, Num: -num.Num, Span: sp}
	case TokHex:
		p.lx.Next()
		return MetaValue{Kind: MetaHex, Hex: t.Hex, Span: t.Span}
	case TokIdent:
		p.lx.Next()
		switch t.Str {
		case "true":
			return MetaValue{Kind: MetaBool, Bool: true, Span: t.Span}
		case "false":
			return MetaValue{Kind: MetaBool, Bool: false, Span: t.Span}
		default:
			return MetaValue{Kind: MetaIdent, Ident: t.Str, Span: t.Span}
		}
	}
	p.errAt(t.Span, "syntax", "expected meta value (string, number, hex, bool, or identifier)", "")
	return MetaValue{}
}

// ---- Const declarations ------------------------------------------------

func (p *parser) parseConstDecl() ConstDecl {
	startTok := p.expectIdent("const", "")
	name := p.expect(TokIdent, "expected identifier after 'const'")
	p.expect(TokColEq, "expected ':=' in const declaration")
	v := p.parseValue()
	sp := startTok.Span
	sp.End = v.Span.End
	return ConstDecl{Name: name.Str, Value: v, Span: sp}
}

// ---- Steps -------------------------------------------------------------

func (p *parser) parseStep() Step {
	idTok := p.expect(TokIdent, "expected step identifier")
	p.expect(TokColEq, fmt.Sprintf("expected ':=' after step id %q", idTok.Str))

	kindTok := p.expect(TokIdent, "expected step kind keyword (local, internal_tool, ...)")

	base := stepBase{id: idTok.Str, span: idTok.Span}

	var step Step
	switch kindTok.Str {
	case "local":
		step = p.parseLocalStep(base)
	case "internal_tool":
		step = p.parseInternalToolStep(base)
	case "client_tool":
		step = p.parseClientToolStep(base)
	case "branch":
		step = p.parseBranchStep(base)
	case "parallel":
		step = p.parseParallelStep(base)
	case "agent":
		step = p.parseAgentStep(base)
	case "ask_user":
		step = p.parseAskUserStep(base)
	case "terminal":
		step = p.parseTerminalStep(base)
	case "escalate":
		step = p.parseEscalateStep(base)
	default:
		p.errAt(kindTok.Span, "unknown_step_kind",
			fmt.Sprintf("unknown step kind %q", kindTok.Str),
			"valid kinds: local, internal_tool, client_tool, branch, parallel, agent, ask_user, terminal, escalate")
	}

	// Trailing clauses: out_bind, control, catch. Order is required:
	// "->" before "=>" before "catch =>". Each is optional per kind; the
	// semantic checker enforces which kinds may have which.
	p.parseStepTrailers(step)

	return step
}

// parseStepTrailers reads any combination of `-> $var`, `=> a [| b]`, and
// `catch => err_step` clauses in any order. Each may appear at most once;
// the semantic checker enforces presence/absence per step kind.
func (p *parser) parseStepTrailers(step Step) {
	base := stepBaseOf(step)
	seenOut, seenCtrl, seenCatch := false, false, false

	for {
		t := p.lx.Peek()
		switch {
		case t.Kind == TokArrow:
			if seenOut {
				p.errAt(t.Span, "syntax", "duplicate '->' clause", "a step may bind one output variable")
			}
			seenOut = true
			p.lx.Next()
			v := p.expect(TokVar, "expected variable name after '->'")
			base.outBind = v.Str
		case t.Kind == TokFatArrow:
			if seenCtrl {
				p.errAt(t.Span, "syntax", "duplicate '=>' clause", "")
			}
			seenCtrl = true
			p.lx.Next()
			first := p.expect(TokIdent, "expected step id after '=>'")
			base.control = []string{first.Str}
			if p.lx.Peek().Kind == TokPipe {
				p.lx.Next()
				second := p.expect(TokIdent, "expected step id after '|'")
				base.control = append(base.control, second.Str)
			}
		case t.Kind == TokIdent && t.Str == "catch":
			if seenCatch {
				p.errAt(t.Span, "syntax", "duplicate 'catch' clause", "")
			}
			seenCatch = true
			p.lx.Next()
			p.expect(TokFatArrow, "expected '=>' after 'catch'")
			err := p.expect(TokIdent, "expected step id after 'catch =>'")
			base.catchTo = err.Str
		default:
			return
		}
	}
}

// stepBaseOf returns the embedded stepBase for any concrete step kind.
// Used by parseStepTrailers and the semantic checker to mutate trailing
// clauses without a giant type switch at every callsite.
func stepBaseOf(s Step) *stepBase {
	switch v := s.(type) {
	case *LocalStep:
		return &v.stepBase
	case *InternalToolStep:
		return &v.stepBase
	case *ClientToolStep:
		return &v.stepBase
	case *BranchStep:
		return &v.stepBase
	case *ParallelStep:
		return &v.stepBase
	case *AgentStep:
		return &v.stepBase
	case *AskUserStep:
		return &v.stepBase
	case *TerminalStep:
		return &v.stepBase
	case *EscalateStep:
		return &v.stepBase
	}
	panic(fmt.Sprintf("unhandled step kind %T", s))
}

// ---- Concrete step parsers ---------------------------------------------

func (p *parser) parseLocalStep(base stepBase) *LocalStep {
	model := p.parseModelID()
	prompt := p.expect(TokString, "expected prompt string after model id")
	kw := p.parseKWArgs()
	return &LocalStep{stepBase: base, ModelID: model, Prompt: prompt.Str, KWArgs: kw}
}

func (p *parser) parseInternalToolStep(base stepBase) *InternalToolStep {
	tool := p.parseToolRef()
	args := p.parseObjLiteral()
	return &InternalToolStep{stepBase: base, Tool: tool, Args: args}
}

func (p *parser) parseClientToolStep(base stepBase) *ClientToolStep {
	tool := p.parseToolRef()
	args := p.parseObjLiteral()
	return &ClientToolStep{stepBase: base, Tool: tool, Args: args}
}

func (p *parser) parseBranchStep(base stepBase) *BranchStep {
	pred := p.parsePredicate()
	return &BranchStep{stepBase: base, Pred: pred}
}

func (p *parser) parseParallelStep(base stepBase) *ParallelStep {
	p.expect(TokLBracket, "expected '[' after 'parallel'")
	ids := p.parseIdentList()
	p.expect(TokRBracket, "expected ']' to close parallel children")
	return &ParallelStep{stepBase: base, Children: ids}
}

func (p *parser) parseAgentStep(base stepBase) *AgentStep {
	profile := p.parseAgentProfile()
	prompt := p.expect(TokString, "expected prompt string after agent profile")
	kw := p.parseKWArgs()
	return &AgentStep{stepBase: base, Profile: profile, Prompt: prompt.Str, KWArgs: kw}
}

func (p *parser) parseAskUserStep(base stepBase) *AskUserStep {
	q := p.expect(TokString, "expected question string after 'ask_user'")
	kw := p.parseKWArgs()
	return &AskUserStep{stepBase: base, Question: q.Str, KWArgs: kw}
}

func (p *parser) parseTerminalStep(base stepBase) *TerminalStep {
	msg := p.expect(TokString, "expected message string after 'terminal'")
	return &TerminalStep{stepBase: base, Message: msg.Str}
}

func (p *parser) parseEscalateStep(base stepBase) *EscalateStep {
	reason := p.expect(TokString, "expected reason string after 'escalate'")
	kw := p.parseKWArgs()
	return &EscalateStep{stepBase: base, Reason: reason.Str, KWArgs: kw}
}

// ---- Model IDs, tool refs, agent profiles ------------------------------

// parseModelID reads a contiguous run of [a-zA-Z0-9_:.-] starting at an
// identifier. The lexer splits this into multiple tokens (e.g. `llama3-8b`
// becomes IDENT(llama3) DASH NUMBER(8) IDENT(b)); we stitch them back into
// a single string by walking byte-contiguous tokens until we hit something
// that is not a letter/digit/separator or that has whitespace before it.
func (p *parser) parseModelID() string {
	first := p.expect(TokIdent, "expected model id")
	out, _ := p.stitchExtendedIdent(first)
	return out
}

// parseToolRef reads `<ident> [':' ident]`. The optional surface (`:client`
// or `:internal`) is validated semantically — here we only require an ident
// after the colon, contiguous in source.
func (p *parser) parseToolRef() ToolRef {
	first := p.expect(TokIdent, "expected tool name")
	tr := ToolRef{Name: first.Str, Span: first.Span}
	t := p.lx.Peek()
	if t.Kind == TokColon && t.Span.Start == first.Span.End {
		colon := p.lx.Next()
		surf := p.lx.Peek()
		if surf.Span.Start != colon.Span.End || surf.Kind != TokIdent {
			p.errAt(colon.Span, "syntax", "expected tool surface after ':' (use ':client' or ':internal')", "")
		}
		p.lx.Next()
		tr.Surface = surf.Str
		tr.Span.End = surf.Span.End
	}
	return tr
}

// parseAgentProfile reads a contiguous extended identifier (same rules as
// parseModelID). Profiles in practice look like `code-reviewer` but the
// grammar accepts arbitrary contiguous letter/digit/dash sequences.
func (p *parser) parseAgentProfile() string {
	first := p.expect(TokIdent, "expected agent profile name")
	out, _ := p.stitchExtendedIdent(first)
	return out
}

// stitchExtendedIdent greedily consumes byte-contiguous IDENT/NUMBER/DASH/
// COLON/DOT tokens after `first` and returns the concatenated source slice
// plus the combined span. It is used for model ids, tool refs (without the
// surface — that's handled separately), agent profiles, and ident-shaped
// values inside lists.
func (p *parser) stitchExtendedIdent(first Token) (string, Span) {
	out := first.Str
	end := first.Span.End
	for {
		t := p.lx.Peek()
		if t.Span.Start != end {
			break
		}
		switch t.Kind {
		case TokIdent, TokNumber:
			p.lx.Next()
			out += t.Str
			end = t.Span.End
		case TokDash:
			p.lx.Next()
			out += "-"
			end = t.Span.End
		case TokDot:
			p.lx.Next()
			out += "."
			end = t.Span.End
		case TokColon:
			// A trailing ':' that is not followed contiguously by an ident or
			// number is part of the next token (e.g. ':' before whitespace
			// could be a meta field separator). We require a contiguous
			// continuation to consume the colon as part of the extended ident.
			p.lx.Next()
			out += ":"
			end = t.Span.End
		default:
			return out, Span{Start: first.Span.Start, End: end, Line: first.Span.Line, Col: first.Span.Col}
		}
	}
	return out, Span{Start: first.Span.Start, End: end, Line: first.Span.Line, Col: first.Span.Col}
}

// ---- KWArgs ------------------------------------------------------------

// parseKWArgs reads zero or more `<ident> = <value>` pairs until it hits
// a terminator. The IDENT-then-COLEQ shape that begins the next step
// (`next_step := ...`) looks deceptively like the start of a kwarg, so we
// peek 2 tokens deep: only consume the IDENT as a kwarg key when the
// token after it is `=` (kwarg) rather than `:=` (next step decl) or
// any other punctuation.
func (p *parser) parseKWArgs() KWArgs {
	var kw KWArgs
	for {
		t := p.lx.Peek()
		if t.Kind != TokIdent {
			break
		}
		if t.Str == "catch" {
			break
		}
		if p.lx.PeekN(1).Kind != TokEq {
			break
		}
		key := p.lx.Next()
		p.expect(TokEq, fmt.Sprintf("expected '=' after kwarg key %q", key.Str))
		v := p.parseValue()
		for _, prev := range kw.Pairs {
			if prev.Key == key.Str {
				p.errAt(key.Span, "syntax", fmt.Sprintf("duplicate keyword arg %q", key.Str), "")
			}
		}
		kw.Pairs = append(kw.Pairs, KWPair{Key: key.Str, Value: v, Span: key.Span})
	}
	return kw
}

// ---- Predicates --------------------------------------------------------

func (p *parser) parsePredicate() Predicate {
	open := p.expect(TokLParen, "expected '(' to begin predicate")
	head := p.expect(TokIdent, "expected predicate operator or combinator")
	switch head.Str {
	case "and":
		operands := p.parseSubPredicates()
		end := p.expect(TokRParen, "expected ')' to close 'and'")
		return PredAnd{Operands: operands, Span: spanFromTo(open.Span, end.Span)}
	case "or":
		operands := p.parseSubPredicates()
		end := p.expect(TokRParen, "expected ')' to close 'or'")
		return PredOr{Operands: operands, Span: spanFromTo(open.Span, end.Span)}
	case "not":
		operand := p.parsePredicate()
		end := p.expect(TokRParen, "expected ')' to close 'not'")
		return PredNot{Operand: operand, Span: spanFromTo(open.Span, end.Span)}
	default:
		var args []PredArg
		for p.lx.Peek().Kind != TokRParen {
			args = append(args, p.parsePredArg())
		}
		end := p.expect(TokRParen, fmt.Sprintf("expected ')' to close predicate '%s'", head.Str))
		return PredAtom{Op: head.Str, Args: args, Span: spanFromTo(open.Span, end.Span)}
	}
}

func (p *parser) parseSubPredicates() []Predicate {
	var preds []Predicate
	for p.lx.Peek().Kind == TokLParen {
		preds = append(preds, p.parsePredicate())
	}
	if len(preds) == 0 {
		t := p.lx.Peek()
		p.errAt(t.Span, "syntax", "combinator requires at least one predicate operand", "")
	}
	return preds
}

func (p *parser) parsePredArg() PredArg {
	t := p.lx.Peek()
	switch t.Kind {
	case TokVar:
		p.lx.Next()
		v := p.parseVarFieldChain(Token{Kind: TokVar, Str: t.Str, Span: t.Span})
		return PredArg{Kind: PredArgVar, Var: &v, Span: v.Span}
	case TokString:
		p.lx.Next()
		return PredArg{Kind: PredArgString, Str: t.Str, Span: t.Span}
	case TokNumber:
		p.lx.Next()
		return PredArg{Kind: PredArgNumber, Num: t.Num, Span: t.Span}
	case TokDash:
		// Negative number.
		dash := p.lx.Next()
		num := p.expect(TokNumber, "expected number after '-'")
		sp := dash.Span
		sp.End = num.Span.End
		return PredArg{Kind: PredArgNumber, Num: -num.Num, Span: sp}
	case TokIdent:
		// Could be: bool, plain ident (model_id, label), or a kw-pair (label=value).
		ident := p.lx.Next()
		switch ident.Str {
		case "true":
			return PredArg{Kind: PredArgBool, Bool: true, Span: ident.Span}
		case "false":
			return PredArg{Kind: PredArgBool, Bool: false, Span: ident.Span}
		}
		if p.lx.Peek().Kind == TokEq {
			p.lx.Next()
			v := p.parseValue()
			sp := ident.Span
			sp.End = v.Span.End
			return PredArg{Kind: PredArgKWPair,
				KW:  &KWPair{Key: ident.Str, Value: v, Span: ident.Span},
				Span: sp}
		}
		// Could be the start of a model_id / tool ref / agent profile / label.
		s, sp := p.stitchExtendedIdent(ident)
		return PredArg{Kind: PredArgIdent, Ident: s, Span: sp}
	case TokLBracket:
		l := p.parseListLiteral()
		return PredArg{Kind: PredArgList, List: l.List, Span: l.Span}
	case TokCmpOp:
		// ==, !=, <=, >=, <, > — only valid as a positional arg of json_path.
		// Carry it through as a PredArgIdent so the semantic checker can
		// validate it appears in a position that expects a comparison op.
		op := p.lx.Next()
		return PredArg{Kind: PredArgIdent, Ident: op.Str, Span: op.Span}
	}
	p.errAt(t.Span, "syntax", "expected predicate argument (var, literal, identifier, or list)", "")
	return PredArg{}
}

// ---- Values, var refs, list/obj literals -------------------------------

func (p *parser) parseValue() Value {
	t := p.lx.Peek()
	switch t.Kind {
	case TokString:
		p.lx.Next()
		return Value{Kind: ValueString, Str: t.Str, Span: t.Span}
	case TokNumber:
		p.lx.Next()
		return Value{Kind: ValueNumber, Num: t.Num, Span: t.Span}
	case TokDash:
		dash := p.lx.Next()
		num := p.expect(TokNumber, "expected number after '-'")
		sp := dash.Span
		sp.End = num.Span.End
		return Value{Kind: ValueNumber, Num: -num.Num, Span: sp}
	case TokHex:
		p.lx.Next()
		return Value{Kind: ValueHex, Hex: t.Hex, Span: t.Span}
	case TokIdent:
		if t.Str == "true" {
			p.lx.Next()
			return Value{Kind: ValueBool, Bool: true, Span: t.Span}
		}
		if t.Str == "false" {
			p.lx.Next()
			return Value{Kind: ValueBool, Bool: false, Span: t.Span}
		}
		// Extended identifier as a value: tool refs (Read, Bash:client),
		// model ids (llama3-8b), label names, agent profiles, etc. The
		// semantic checker resolves what this references in context.
		first := p.lx.Next()
		s, sp := p.stitchExtendedIdent(first)
		return Value{Kind: ValueIdent, Ident: s, Span: sp}
	case TokLBracket:
		return p.parseListLiteral()
	case TokLBrace:
		return p.parseObjLiteralAsValue()
	case TokVar:
		p.lx.Next()
		v := p.parseVarFieldChain(t)
		return Value{Kind: ValueVar, Var: &v, Span: v.Span}
	}
	p.errAt(t.Span, "syntax", "expected a value", "")
	return Value{}
}

func (p *parser) parseListLiteral() Value {
	open := p.expect(TokLBracket, "expected '['")
	v := Value{Kind: ValueList, Span: open.Span}
	if p.lx.Peek().Kind == TokRBracket {
		end := p.lx.Next()
		v.Span.End = end.Span.End
		return v
	}
	for {
		v.List = append(v.List, p.parseValue())
		if p.lx.Peek().Kind == TokComma {
			p.lx.Next()
			continue
		}
		break
	}
	end := p.expect(TokRBracket, "expected ']' to close list literal")
	v.Span.End = end.Span.End
	return v
}

// parseObjLiteralAsValue wraps the obj literal returned by parseObjLiteral
// into a Value carrier, used when the parser needs an obj as a value.
func (p *parser) parseObjLiteralAsValue() Value {
	o := p.parseObjLiteral()
	return Value{Kind: ValueObj, Obj: o, Span: o.Span}
}

func (p *parser) parseObjLiteral() ObjLiteral {
	open := p.expect(TokLBrace, "expected '{' to begin object literal")
	o := ObjLiteral{Span: open.Span}
	if p.lx.Peek().Kind == TokRBrace {
		end := p.lx.Next()
		o.Span.End = end.Span.End
		return o
	}
	for {
		key := p.expect(TokIdent, "expected key (identifier) in object literal")
		p.expect(TokColon, fmt.Sprintf("expected ':' after key %q", key.Str))
		val := p.parseValue()
		o.Pairs = append(o.Pairs, ObjPair{Key: key.Str, Value: val,
			Span: spanFromTo(key.Span, val.Span)})
		if p.lx.Peek().Kind == TokComma {
			p.lx.Next()
			continue
		}
		break
	}
	end := p.expect(TokRBrace, "expected '}' to close object literal")
	o.Span.End = end.Span.End
	return o
}

// parseVarFieldChain consumes the .field.subfield chain after a $var token.
// The $var token (TokVar with Str=name) is expected to have already been
// consumed by the caller — passed in as `head`.
func (p *parser) parseVarFieldChain(head Token) VarRef {
	v := VarRef{Name: head.Str, Span: head.Span}
	for {
		t := p.lx.Peek()
		// Field chain is only continuous with no whitespace; Span.Start == previous End.
		if t.Kind != TokDot || t.Span.Start != v.Span.End {
			break
		}
		dot := p.lx.Next()
		next := p.lx.Peek()
		if next.Kind != TokIdent || next.Span.Start != dot.Span.End {
			p.errAt(dot.Span, "syntax", "expected field name after '.'", "")
		}
		p.lx.Next()
		v.Fields = append(v.Fields, next.Str)
		v.Span.End = next.Span.End
	}
	return v
}

func (p *parser) parseIdentList() []string {
	var ids []string
	if p.lx.Peek().Kind == TokRBracket {
		return ids
	}
	for {
		t := p.expect(TokIdent, "expected step identifier")
		ids = append(ids, t.Str)
		if p.lx.Peek().Kind == TokComma {
			p.lx.Next()
			continue
		}
		return ids
	}
}

// ---- Helpers -----------------------------------------------------------

func (p *parser) expect(k TokKind, msg string) Token {
	t := p.lx.Next()
	if t.Kind != k {
		if msg == "" {
			msg = fmt.Sprintf("expected %s, got %s", k, t.Kind)
		}
		p.errAt(t.Span, "syntax", msg, "")
	}
	return t
}

func (p *parser) expectIdent(name, msg string) Token {
	t := p.lx.Next()
	if t.Kind != TokIdent || t.Str != name {
		if msg == "" {
			msg = fmt.Sprintf("expected %q, got %q", name, t.Str)
		}
		p.errAt(t.Span, "syntax", msg, "")
	}
	return t
}

func (p *parser) errAt(span Span, category, msg, hint string) {
	// Surface lexer errors first if they're more specific.
	if le := p.lx.Err(); le != nil {
		panic(parseError{err: le})
	}
	panic(parseError{err: &Error{
		Category: category,
		Message:  msg,
		Hint:     hint,
		Span:     span,
		Source:   p.src,
	}})
}

func spanFromTo(a, b Span) Span {
	return Span{Start: a.Start, End: b.End, Line: a.Line, Col: a.Col}
}
