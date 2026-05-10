package plan

import (
	"fmt"
	"strings"
)

// CheckContext supplies optional external knowledge the semantic checker can
// validate references against. Empty fields skip the corresponding check —
// useful for unit-testing the checker without manifest/tool plumbing.
type CheckContext struct {
	KnownModels        []string
	KnownClientTools   []string
	KnownInternalTools []string
	KnownAgentProfiles []string
	ExpectedManifest   string // hex string with 0x prefix, empty to skip
}

// Check runs semantic validation on a parsed plan, returning all diagnostics
// found. An empty slice means the plan is valid. The slice is ordered by
// source position so the first error in the diff is the most contextually
// useful for Opus to correct.
func Check(p *Plan, ctx CheckContext) []*Error {
	c := &checker{
		plan:     p,
		src:      pickSource(p),
		stepByID: map[string]Step{},
		binderOf: map[string]Step{},
		ctx:      ctx,
	}
	c.run()
	return c.errs
}

type checker struct {
	plan *Plan
	src  string
	errs []*Error
	ctx  CheckContext

	stepByID map[string]Step
	binderOf map[string]Step // out-bind variable name → step
}

// pickSource recovers the original source string from any error already on
// the plan (errors carry .Source). Used so checker errors render with the
// same source listing the parser would.
func pickSource(p *Plan) string {
	// The parser stores .Source on errors but not on the plan; we don't have
	// the source string here unless callers pass it. The renderer falls back
	// to span line/col when Source is empty, so this is a soft degradation.
	_ = p
	return ""
}

// SetSource attaches the original source to a slice of checker errors so
// they render with source-line snippets. Call this after Check() at the
// callsite that has the source string.
func SetSource(errs []*Error, src string) {
	for _, e := range errs {
		if e.Source == "" {
			e.Source = src
		}
	}
}

// ---- Top-level orchestration -------------------------------------------

func (c *checker) run() {
	c.indexSteps()
	if len(c.errs) > 0 {
		return // duplicate IDs would make the rest of the analysis meaningless
	}
	c.checkMeta()
	c.checkConsts()
	for _, s := range c.plan.Steps {
		c.checkStepShape(s)
	}
	c.checkReferences()
	c.checkAcyclic()
	c.checkDataflow()
}

// ---- Indexing ----------------------------------------------------------

func (c *checker) indexSteps() {
	for _, s := range c.plan.Steps {
		id := s.StepID()
		if _, dup := c.stepByID[id]; dup {
			c.err(s.StepSpan(), "duplicate_id", fmt.Sprintf("duplicate step id %q", id),
				"each step must have a unique identifier")
			continue
		}
		c.stepByID[id] = s

		if strings.HasPrefix(id, "__") {
			c.err(s.StepSpan(), "reserved_identifier",
				fmt.Sprintf("step id %q uses reserved prefix '__'", id),
				"identifiers starting with '__' are reserved for orchestrator-injected steps")
		}

		if v := s.OutBind(); v != "" {
			if isReservedVar(v) {
				c.err(s.StepSpan(), "reserved_identifier",
					fmt.Sprintf("variable name %q is reserved", v),
					"$err and its fields are auto-bound inside catch handlers; pick a different name")
			}
			if prev, dup := c.binderOf[v]; dup {
				c.err(s.StepSpan(), "duplicate_binding",
					fmt.Sprintf("variable $%s is bound by both %q and %q", v, prev.StepID(), id),
					"each variable may be bound at most once across the plan")
				continue
			}
			c.binderOf[v] = s
		}
	}
}

func isReservedVar(v string) bool {
	return v == "err" || strings.HasPrefix(v, "err.") || strings.HasPrefix(v, "__")
}

// ---- Meta --------------------------------------------------------------

func (c *checker) checkMeta() {
	m := c.plan.Meta

	entryV, ok := m.Fields["entry"]
	if !ok {
		c.err(m.Span, "missing_meta", "meta block missing required field 'entry'",
			"add 'entry: <step_id>' to the meta block")
	} else if entryV.Kind != MetaIdent {
		c.err(entryV.Span, "type_mismatch", "'entry' must be a step identifier", "")
	} else if _, ok := c.stepByID[entryV.Ident]; !ok {
		c.err(entryV.Span, "unresolved_reference",
			fmt.Sprintf("entry references unknown step %q", entryV.Ident), "")
	}

	if v, ok := m.Fields["on_err"]; ok {
		if v.Kind != MetaIdent {
			c.err(v.Span, "type_mismatch", "'on_err' must be a step identifier", "")
		} else if _, ok := c.stepByID[v.Ident]; !ok {
			c.err(v.Span, "unresolved_reference",
				fmt.Sprintf("on_err references unknown step %q", v.Ident), "")
		}
	}

	// Manifest hash check.
	if c.ctx.ExpectedManifest != "" {
		v, ok := m.Fields["manifest"]
		if !ok {
			c.err(m.Span, "manifest_mismatch", "meta missing 'manifest' field",
				fmt.Sprintf("add 'manifest: %s' so the orchestrator can verify the plan was produced against the current manifest", c.ctx.ExpectedManifest))
		} else if v.Kind != MetaHex {
			c.err(v.Span, "type_mismatch", "'manifest' must be a hex literal (e.g. 0xa1b2c3d4)", "")
		} else {
			got := fmt.Sprintf("0x%x", v.Hex)
			if got != c.ctx.ExpectedManifest {
				c.err(v.Span, "manifest_mismatch",
					fmt.Sprintf("manifest hash %s does not match orchestrator's current %s", got, c.ctx.ExpectedManifest),
					"the manifest changed between plan generation and execution; abort and emit a fresh plan")
			}
		}
	}
}

// ---- Const decls -------------------------------------------------------

func (c *checker) checkConsts() {
	seen := map[string]bool{}
	for _, cd := range c.plan.Consts {
		if strings.HasPrefix(cd.Name, "__") {
			c.err(cd.Span, "reserved_identifier",
				fmt.Sprintf("const name %q uses reserved prefix '__'", cd.Name), "")
		}
		if isReservedVar(cd.Name) {
			c.err(cd.Span, "reserved_identifier",
				fmt.Sprintf("const name %q shadows reserved variable", cd.Name), "")
		}
		if seen[cd.Name] {
			c.err(cd.Span, "duplicate_id",
				fmt.Sprintf("duplicate const declaration %q", cd.Name), "")
		}
		seen[cd.Name] = true
		if _, conflict := c.binderOf[cd.Name]; conflict {
			c.err(cd.Span, "duplicate_binding",
				fmt.Sprintf("const %q conflicts with a step out-bind of the same name", cd.Name), "")
		}
	}
}

// ---- Step shape (control / out_bind / catch presence per kind) ---------

func (c *checker) checkStepShape(s Step) {
	kind := s.StepKind()
	out := s.OutBind()
	ctrl := s.Control()
	catch := s.Catch()

	switch kind {
	case "local", "internal_tool", "client_tool", "agent", "ask_user":
		if out == "" {
			c.err(s.StepSpan(), "missing_clause",
				fmt.Sprintf("%s step must bind an output variable with '-> $name'", kind), "")
		}
		if len(ctrl) != 1 {
			c.err(s.StepSpan(), "missing_clause",
				fmt.Sprintf("%s step must declare exactly one '=> next' target (got %d)", kind, len(ctrl)), "")
		}
	case "branch":
		if out != "" {
			c.err(s.StepSpan(), "invalid_clause",
				"branch steps cannot bind an output variable",
				"branch evaluates a predicate and selects a successor; remove the '-> $name'")
		}
		if len(ctrl) != 2 {
			c.err(s.StepSpan(), "missing_clause",
				fmt.Sprintf("branch must declare two control targets '=> A | B' (got %d)", len(ctrl)), "")
		}
	case "parallel":
		if out != "" {
			c.err(s.StepSpan(), "invalid_clause", "parallel steps cannot bind an output variable",
				"each child step binds its own output; the merge step reads them")
		}
		if len(ctrl) != 1 {
			c.err(s.StepSpan(), "missing_clause",
				fmt.Sprintf("parallel must declare a single merge target '=> merge_id' (got %d)", len(ctrl)), "")
		}
		ps := s.(*ParallelStep)
		if len(ps.Children) < 2 {
			c.err(s.StepSpan(), "invalid_clause",
				"parallel must declare at least two children",
				"a single-child parallel is equivalent to a linear step")
		}
	case "escalate":
		if out != "" {
			c.err(s.StepSpan(), "invalid_clause", "escalate steps cannot bind an output variable", "")
		}
		if len(ctrl) > 1 {
			c.err(s.StepSpan(), "invalid_clause",
				fmt.Sprintf("escalate may declare at most one resume target (got %d)", len(ctrl)), "")
		}
	case "terminal":
		if out != "" {
			c.err(s.StepSpan(), "invalid_clause", "terminal steps cannot bind an output variable", "")
		}
		if len(ctrl) != 0 {
			c.err(s.StepSpan(), "invalid_clause",
				"terminal steps cannot declare a control target",
				"terminal ends the plan; remove the '=>' clause")
		}
		if catch != "" {
			c.err(s.StepSpan(), "invalid_clause",
				"terminal steps cannot declare a catch handler", "")
		}
	}

	// Step-specific extra checks
	switch v := s.(type) {
	case *LocalStep:
		// `inputs` is accepted but is a no-op on `local` (the runtime
		// resolves $vars via prompt interpolation, not via a separate
		// declaration). We allow it because the planner sometimes
		// emits it by analogy with `agent` steps; rejecting forced
		// every such plan to fall back to passthrough.
		c.checkKWArgsAllowed(v.KWArgs, kind, []string{"tools", "iter", "timeout_ms", "format", "inputs"})
		if c.ctx.KnownModels != nil {
			if !contains(c.ctx.KnownModels, v.ModelID) {
				c.err(v.StepSpan(), "unresolved_reference",
					fmt.Sprintf("unknown local model %q", v.ModelID),
					"models must appear in ~/.intern/local_models.yaml")
			}
		}
		c.checkLocalToolsKWArg(v)
	case *InternalToolStep:
		c.checkToolRef(v.Tool, true)
	case *ClientToolStep:
		c.checkToolRef(v.Tool, false)
	case *AgentStep:
		c.checkKWArgsAllowed(v.KWArgs, kind, []string{"inputs", "max_iter", "timeout_ms"})
		if c.ctx.KnownAgentProfiles != nil && !contains(c.ctx.KnownAgentProfiles, v.Profile) {
			c.err(v.StepSpan(), "unresolved_reference",
				fmt.Sprintf("unknown agent profile %q", v.Profile), "")
		}
	case *AskUserStep:
		c.checkKWArgsAllowed(v.KWArgs, kind, []string{"choices"})
	case *EscalateStep:
		c.checkKWArgsAllowed(v.KWArgs, kind, []string{"ctx"})
	}
}

func (c *checker) checkLocalToolsKWArg(s *LocalStep) {
	v, ok := s.KWArgs.Get("tools")
	if !ok {
		return
	}
	if v.Kind != ValueList {
		c.err(v.Span, "type_mismatch", "tools= must be a list", "")
		return
	}
	for _, item := range v.List {
		if item.Kind != ValueIdent {
			c.err(item.Span, "type_mismatch", "tools list entries must be tool refs", "")
			continue
		}
		// Parse Name[:Surface]
		name := item.Ident
		surface := ""
		if i := strings.Index(item.Ident, ":"); i >= 0 {
			name = item.Ident[:i]
			surface = item.Ident[i+1:]
		}
		if surface != "" && surface != "client" && surface != "internal" {
			c.err(item.Span, "invalid_clause",
				fmt.Sprintf("invalid tool surface %q (use ':client' or ':internal')", surface), "")
		}
		// Tool name resolution against known sets — soft check only because
		// tool refs inside local steps default by surface and either set may
		// validly contain them.
		if c.ctx.KnownInternalTools != nil && c.ctx.KnownClientTools != nil {
			if !contains(c.ctx.KnownInternalTools, name) && !contains(c.ctx.KnownClientTools, name) {
				c.err(item.Span, "unresolved_reference",
					fmt.Sprintf("unknown tool %q", name), "")
			}
		}
	}
}

func (c *checker) checkToolRef(t ToolRef, internalContext bool) {
	if t.Surface != "" && t.Surface != "client" && t.Surface != "internal" {
		c.err(t.Span, "invalid_clause",
			fmt.Sprintf("invalid tool surface %q", t.Surface), "")
	}
	if internalContext {
		if c.ctx.KnownInternalTools != nil && !contains(c.ctx.KnownInternalTools, t.Name) {
			c.err(t.Span, "unresolved_reference",
				fmt.Sprintf("internal_tool references unknown tool %q", t.Name),
				"this tool must be in ~/.intern/permissions.yaml's internal allowlist")
		}
	} else {
		if c.ctx.KnownClientTools != nil && !contains(c.ctx.KnownClientTools, t.Name) {
			c.err(t.Span, "unresolved_reference",
				fmt.Sprintf("client_tool references unknown tool %q", t.Name),
				"the client must advertise this tool in its request 'tools' list")
		}
	}
}

func (c *checker) checkKWArgsAllowed(kw KWArgs, kind string, allowed []string) {
	for _, p := range kw.Pairs {
		if !contains(allowed, p.Key) {
			c.err(p.Span, "unknown_kwarg",
				fmt.Sprintf("unknown kwarg %q for %s step", p.Key, kind),
				fmt.Sprintf("allowed kwargs for %s: %s", kind, strings.Join(allowed, ", ")))
		}
	}
}

// ---- Reference resolution ----------------------------------------------

func (c *checker) checkReferences() {
	for _, s := range c.plan.Steps {
		// Control targets
		for _, t := range s.Control() {
			if _, ok := c.stepByID[t]; !ok {
				c.err(s.StepSpan(), "unresolved_reference",
					fmt.Sprintf("step %q references unknown control target %q", s.StepID(), t),
					"")
			}
		}
		// Catch target
		if t := s.Catch(); t != "" {
			if _, ok := c.stepByID[t]; !ok {
				c.err(s.StepSpan(), "unresolved_reference",
					fmt.Sprintf("step %q catch references unknown step %q", s.StepID(), t),
					"")
			}
		}
		// Parallel children
		if ps, ok := s.(*ParallelStep); ok {
			for _, ch := range ps.Children {
				if _, ok := c.stepByID[ch]; !ok {
					c.err(ps.StepSpan(), "unresolved_reference",
						fmt.Sprintf("parallel %q references unknown child %q", ps.StepID(), ch),
						"")
				}
			}
		}
	}
}

// ---- DAG check ---------------------------------------------------------

// successors returns the set of step IDs reachable in one hop from s by
// following control edges, parallel children, parallel→merge, catch, and
// escalate-resume. Used for cycle detection — the global on_err edge is
// deliberately *not* included here so an escalate-with-resume that
// re-enters the main graph isn't flagged as a cycle.
func (c *checker) successors(s Step) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, t := range s.Control() {
		add(t)
	}
	if cv := s.Catch(); cv != "" {
		add(cv)
	}
	if ps, ok := s.(*ParallelStep); ok {
		for _, ch := range ps.Children {
			add(ch)
		}
	}
	return out
}

// dataflowSuccessors is successors() plus the implicit edge from any
// step to the global meta.on_err target. Runtime semantics are "any
// step's failure routes to on_err", so dataflow reachability has to
// model that path — otherwise an escalate step that references vars
// bound earlier in the plan is rejected as unreachable_read even though
// those vars are always bound by the time on_err fires.
func (c *checker) dataflowSuccessors(s Step) []string {
	out := c.successors(s)
	if onErr, ok := c.plan.Meta.Fields["on_err"]; ok && onErr.Kind == MetaIdent && onErr.Ident != s.StepID() {
		for _, existing := range out {
			if existing == onErr.Ident {
				return out
			}
		}
		out = append(out, onErr.Ident)
	}
	return out
}

func (c *checker) checkAcyclic() {
	// Color: 0 unvisited, 1 in-progress, 2 done.
	color := map[string]int{}
	var dfs func(id string) bool
	dfs = func(id string) bool {
		if color[id] == 1 {
			c.err(c.stepByID[id].StepSpan(), "cycle_detected",
				fmt.Sprintf("control flow contains a cycle through step %q", id),
				"plans must be DAGs in v1; express iteration via local steps' iter= budget instead")
			return true
		}
		if color[id] == 2 {
			return false
		}
		color[id] = 1
		s, ok := c.stepByID[id]
		if !ok {
			return false
		}
		for _, succ := range c.successors(s) {
			if dfs(succ) {
				return true
			}
		}
		color[id] = 2
		return false
	}
	// Start from entry, then any unvisited step (catches dead components).
	if entry, ok := c.plan.Meta.Fields["entry"]; ok && entry.Kind == MetaIdent {
		dfs(entry.Ident)
	}
	for id := range c.stepByID {
		if color[id] == 0 {
			dfs(id)
		}
	}
}

// ---- Dataflow ----------------------------------------------------------

// checkDataflow verifies every $var read in the plan refers to a binding step
// reachable from entry, and that the reading step is reachable from the
// binding step. This is a permissive check (does not catch path-dependent
// gaps where one branch arm fails to bind a var the other arm binds and a
// downstream step reads); a full dominator pass is deferred to a future
// version.
func (c *checker) checkDataflow() {
	entryID := ""
	if e, ok := c.plan.Meta.Fields["entry"]; ok && e.Kind == MetaIdent {
		entryID = e.Ident
	}
	if entryID == "" {
		return // entry-resolution error already reported
	}

	constNames := map[string]bool{}
	for _, cd := range c.plan.Consts {
		constNames[cd.Name] = true
	}

	for _, s := range c.plan.Steps {
		c.collectAndCheckReads(s, entryID, constNames)
	}
}

// collectAndCheckReads walks all $var references inside step s and validates
// each one against entryID-reachability and binder-availability.
func (c *checker) collectAndCheckReads(s Step, entryID string, constNames map[string]bool) {
	visit := func(v *VarRef) {
		// $err is auto-bound inside catch handlers (the immediately catching step).
		if v.Name == "err" {
			return
		}
		if constNames[v.Name] {
			return
		}
		binder, ok := c.binderOf[v.Name]
		if !ok {
			c.err(v.Span, "unresolved_reference",
				fmt.Sprintf("variable $%s is read but never bound", v.Name),
				"some prior step must bind this variable via '-> $"+v.Name+"'")
			return
		}
		// A step cannot read its own output before the step runs.
		if binder.StepID() == s.StepID() {
			c.err(v.Span, "self_reference",
				fmt.Sprintf("step %q reads its own output $%s", s.StepID(), v.Name), "")
			return
		}
		if !c.reachable(entryID, binder.StepID()) {
			c.err(v.Span, "unreachable_binding",
				fmt.Sprintf("$%s is bound by %q but that step is unreachable from entry", v.Name, binder.StepID()),
				"add a path from the entry step to the binder, or remove the unused binding")
			return
		}
		if !c.reachable(binder.StepID(), s.StepID()) {
			c.err(v.Span, "unreachable_read",
				fmt.Sprintf("$%s is bound by %q but reader %q is not reachable from the binder", v.Name, binder.StepID(), s.StepID()),
				"reorder steps so the binder runs before the reader on every path")
			return
		}
	}

	c.walkStepVars(s, visit)
}

// walkStepVars visits every VarRef and every interpolation in the step's
// inputs (prompt strings, kwarg values, predicate args, escalate context).
func (c *checker) walkStepVars(s Step, visit func(*VarRef)) {
	switch v := s.(type) {
	case *LocalStep:
		c.walkStringInterp(v.Prompt, v.StepSpan(), visit)
		c.walkKWArgs(v.KWArgs, visit)
	case *InternalToolStep:
		c.walkObjLiteral(v.Args, visit)
	case *ClientToolStep:
		c.walkObjLiteral(v.Args, visit)
	case *BranchStep:
		c.walkPredicate(v.Pred, visit)
	case *AgentStep:
		c.walkStringInterp(v.Prompt, v.StepSpan(), visit)
		c.walkKWArgs(v.KWArgs, visit)
	case *AskUserStep:
		c.walkStringInterp(v.Question, v.StepSpan(), visit)
		c.walkKWArgs(v.KWArgs, visit)
	case *TerminalStep:
		c.walkStringInterp(v.Message, v.StepSpan(), visit)
	case *EscalateStep:
		c.walkStringInterp(v.Reason, v.StepSpan(), visit)
		c.walkKWArgs(v.KWArgs, visit)
	}
}

func (c *checker) walkKWArgs(kw KWArgs, visit func(*VarRef)) {
	for _, p := range kw.Pairs {
		c.walkValue(p.Value, visit)
	}
}

func (c *checker) walkObjLiteral(o ObjLiteral, visit func(*VarRef)) {
	for _, p := range o.Pairs {
		c.walkValue(p.Value, visit)
	}
}

func (c *checker) walkValue(v Value, visit func(*VarRef)) {
	switch v.Kind {
	case ValueString:
		c.walkStringInterp(v.Str, v.Span, visit)
	case ValueList:
		for _, e := range v.List {
			c.walkValue(e, visit)
		}
	case ValueObj:
		c.walkObjLiteral(v.Obj, visit)
	case ValueVar:
		visit(v.Var)
	}
}

func (c *checker) walkPredicate(p Predicate, visit func(*VarRef)) {
	switch v := p.(type) {
	case PredAtom:
		for _, a := range v.Args {
			if a.Kind == PredArgVar && a.Var != nil {
				visit(a.Var)
			}
			if a.Kind == PredArgList {
				for _, item := range a.List {
					c.walkValue(item, visit)
				}
			}
			if a.Kind == PredArgKWPair && a.KW != nil {
				c.walkValue(a.KW.Value, visit)
			}
		}
	case PredAnd:
		for _, op := range v.Operands {
			c.walkPredicate(op, visit)
		}
	case PredOr:
		for _, op := range v.Operands {
			c.walkPredicate(op, visit)
		}
	case PredNot:
		c.walkPredicate(v.Operand, visit)
	}
}

// walkStringInterp scans s for ${name[.field]+} interpolations, computing a
// best-effort span for each by offsetting from the host literal's span.
// Unparseable interpolations emit a syntax error.
func (c *checker) walkStringInterp(s string, host Span, visit func(*VarRef)) {
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 >= len(s) || s[i+1] != '{' {
			continue
		}
		// Find matching closing '}'.
		end := strings.Index(s[i+2:], "}")
		if end < 0 {
			c.err(host, "syntax",
				"unterminated ${...} interpolation in string", "")
			return
		}
		inner := s[i+2 : i+2+end]
		parts := strings.Split(inner, ".")
		if parts[0] == "" {
			c.err(host, "syntax",
				"empty interpolation '${}' in string", "")
			i = i + 2 + end
			continue
		}
		v := &VarRef{Name: parts[0], Fields: parts[1:], Span: host}
		visit(v)
		i = i + 2 + end
	}
}

// ---- Reachability cache (BFS) ------------------------------------------

func (c *checker) reachable(from, to string) bool {
	if from == to {
		return true
	}
	visited := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		s, ok := c.stepByID[cur]
		if !ok {
			continue
		}
		for _, succ := range c.dataflowSuccessors(s) {
			if succ == to {
				return true
			}
			if !visited[succ] {
				visited[succ] = true
				queue = append(queue, succ)
			}
		}
	}
	return false
}

// ---- Helpers -----------------------------------------------------------

func (c *checker) err(span Span, category, msg, hint string) {
	c.errs = append(c.errs, &Error{
		Category: category,
		Message:  msg,
		Hint:     hint,
		Span:     span,
		Source:   c.src,
	})
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
