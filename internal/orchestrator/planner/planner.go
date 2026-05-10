// Package planner assembles the system prompt block intern injects into
// outgoing requests when --orchestrate=on. The block instructs Opus to emit
// a plan@v1 program via the `emit_plan` tool. Stable byte content across
// requests within a manifest_hash is the cache-hit contract; placeholders
// here are deterministic functions of the manifest, permissions, and
// sniffed client tools.
package planner

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

//go:embed template.txt
var rawTemplate string

// AgentProfile is one entry in the {{AGENT_PROFILES}} table. Profiles
// mirror the parent harness's subagent registry; intern doesn't host its
// own subagents, so this list is set at build time and not read from disk.
type AgentProfile struct {
	Name        string
	Description string
}

// DefaultAgentProfiles is the static set advertised to the planner. New
// profiles must be added here and behind a manifest-hash bump (the planner
// block bytes change, invalidating prompt cache).
var DefaultAgentProfiles = []AgentProfile{
	{"code-reviewer", "Senior code review: bugs, style, security, performance."},
	{"bug-finder", "Logic errors, race conditions, edge cases, nil derefs."},
	{"security-auditor", "OWASP Top 10, injection, auth, crypto misuse."},
	{"Explore", "Fast read-only code search. Good for \"where is X\" queries."},
	{"general-purpose", "Multi-step research and code exploration."},
}

// ClientTool is one entry in the {{CLIENT_TOOLS_TABLE}}. Sniffed from the
// incoming request's `tools` field at planning time; the schema column
// is a one-line summary, not the full JSON Schema.
type ClientTool struct {
	Name   string
	Schema string
}

// DefaultClientTools is the typical Claude Code surface. The proxy
// replaces this with whatever it sniffs from the live request, but
// callers without sniffing capability (CLI `intern plan`) get this
// representative set.
var DefaultClientTools = []ClientTool{
	{"Read", "{file_path: string, offset?: int, limit?: int}"},
	{"Edit", "{file_path: string, old_string: string, new_string: string, replace_all?: bool}"},
	{"Write", "{file_path: string, content: string}"},
	{"Bash", "{command: string, description: string, run_in_background?: bool, timeout?: int}"},
	{"Grep", "{pattern: string, path?: string, glob?: string, ...}"},
	{"Glob", "{pattern: string, path?: string}"},
	{"Task", "{description: string, prompt: string, subagent_type?: string}"},
	{"AskUserQuestion", "{question: string, choices?: [string]}"},
}

// HardClamps mirrors the executor-side numeric limits. The planner
// surfaces these so Opus does not declare values it can't actually use;
// an out-of-range plan is accepted but silently clamped, with a metric.
type HardClamps struct {
	MaxEscMin, MaxEscMax       int
	MaxAgentsMin, MaxAgentsMax int
	MaxLocalMin, MaxLocalMax   int
	BudgetUSDMin, BudgetUSDMax float64
	LocalIterMin, LocalIterMax int
	AgentIterMin, AgentIterMax int
}

// DefaultHardClamps mirrors the values in the grammar spec.
var DefaultHardClamps = HardClamps{
	MaxEscMin: 0, MaxEscMax: 3,
	MaxAgentsMin: 0, MaxAgentsMax: 8,
	MaxLocalMin: 0, MaxLocalMax: 16,
	BudgetUSDMin: 0.0, BudgetUSDMax: 10.0,
	LocalIterMin: 1, LocalIterMax: 10,
	AgentIterMin: 1, AgentIterMax: 20,
}

// Context bundles the inputs needed to render the prompt template.
// The orchestrator constructs one of these per planning call; the
// rendered output is the load-bearing prompt-cache prefix.
type Context struct {
	Manifest       *manifest.Manifest
	Permissions   *manifest.Permissions
	ClientTools    []ClientTool
	AgentProfiles  []AgentProfile
	Clamps         HardClamps
}

// Render expands the template against ctx, returning the system prompt
// block. The output is byte-stable for any fixed Context — sort all
// model/tool/profile lists before passing in if you want hash stability
// across runs that may receive them in varying order.
func Render(ctx Context) (string, error) {
	if ctx.Manifest == nil {
		return "", fmt.Errorf("planner: nil manifest")
	}
	if ctx.Permissions == nil {
		return "", fmt.Errorf("planner: nil permissions")
	}
	if ctx.AgentProfiles == nil {
		ctx.AgentProfiles = DefaultAgentProfiles
	}
	if ctx.ClientTools == nil {
		ctx.ClientTools = DefaultClientTools
	}
	zeroClamps := HardClamps{}
	if ctx.Clamps == zeroClamps {
		ctx.Clamps = DefaultHardClamps
	}

	out := rawTemplate
	out = strings.ReplaceAll(out, "{{MANIFEST_HASH}}", ctx.Manifest.Hash)
	out = strings.ReplaceAll(out, "{{LOCAL_MODELS_TABLE}}", renderModelsTable(ctx.Manifest))
	out = strings.ReplaceAll(out, "{{CLIENT_TOOLS_TABLE}}", renderClientToolsTable(ctx.ClientTools))
	out = strings.ReplaceAll(out, "{{INTERN_TOOLS_TABLE}}", renderInternalToolsTable(ctx.Permissions))
	out = strings.ReplaceAll(out, "{{AGENT_PROFILES}}", renderAgentProfilesTable(ctx.AgentProfiles))
	out = strings.ReplaceAll(out, "{{HARD_CLAMPS}}", renderClamps(ctx.Clamps))
	return out, nil
}

// ---- Table renderers ---------------------------------------------------

// renderModelsTable produces a markdown-style pipe table for the local
// models registry. Rows are sorted by model id so the output is
// independent of YAML key order.
func renderModelsTable(m *manifest.Manifest) string {
	if len(m.Models) == 0 {
		return "_No local models configured. Add entries to ~/.intern/local_models.yaml; until then, use `agent` and direct cloud calls only._"
	}
	models := make([]manifest.Model, len(m.Models))
	copy(models, m.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "| id\t| capabilities\t| anti-capabilities\t| max_input\t| latency_ms\t| description |")
	fmt.Fprintln(w, "|---\t|---\t|---\t|---\t|---\t|---|")
	for _, mod := range models {
		fmt.Fprintf(w, "| %s\t| %s\t| %s\t| %d\t| %d\t| %s |\n",
			mod.ID,
			strings.Join(mod.Capabilities, ", "),
			strings.Join(mod.AntiCapabilities, ", "),
			mod.MaxInputTokens,
			mod.AvgLatencyMS,
			mod.Description)
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

// renderClientToolsTable is the table for tools the user's IDE advertises.
// We sort by name for byte stability.
func renderClientToolsTable(tools []ClientTool) string {
	if len(tools) == 0 {
		return "_No client tools advertised. `client_tool` and `agent` steps are unavailable._"
	}
	cp := make([]ClientTool, len(tools))
	copy(cp, tools)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })

	var b strings.Builder
	fmt.Fprintln(&b, "| tool_name | input schema (summary) |")
	fmt.Fprintln(&b, "|---|---|")
	for _, t := range cp {
		fmt.Fprintf(&b, "| %s | %s |\n", t.Name, t.Schema)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderInternalToolsTable is the table for tools intern is allowed to
// invoke silently. Bash-with-allowlist gets a special render so the
// planner sees the actual command set rather than a vague "Bash".
func renderInternalToolsTable(p *manifest.Permissions) string {
	if len(p.Internal) == 0 {
		return "_No internal tools permitted. `internal_tool` steps are unavailable._"
	}
	perms := make([]manifest.ToolPermission, len(p.Internal))
	copy(perms, p.Internal)
	sort.Slice(perms, func(i, j int) bool { return perms[i].Name < perms[j].Name })

	var b strings.Builder
	fmt.Fprintln(&b, "| tool_name | permission |")
	fmt.Fprintln(&b, "|---|---|")
	for _, t := range perms {
		desc := ""
		switch t.Name {
		case "Read":
			desc = "full filesystem read"
		case "Grep":
			desc = "full filesystem grep"
		case "Glob":
			desc = "full filesystem glob"
		case "Bash":
			if len(t.BashAllowlist) == 0 {
				desc = "unrestricted shell"
			} else {
				al := make([]string, len(t.BashAllowlist))
				copy(al, t.BashAllowlist)
				sort.Strings(al)
				desc = "allowlist: " + strings.Join(al, ", ")
			}
		default:
			desc = "permitted"
		}
		fmt.Fprintf(&b, "| %s | %s |\n", t.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderAgentProfilesTable renders the static AGENT_PROFILES list.
func renderAgentProfilesTable(profiles []AgentProfile) string {
	if len(profiles) == 0 {
		return "_No agent profiles available. `agent` steps are unavailable._"
	}
	cp := make([]AgentProfile, len(profiles))
	copy(cp, profiles)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Name < cp[j].Name })

	var b strings.Builder
	fmt.Fprintln(&b, "| profile | description |")
	fmt.Fprintln(&b, "|---|---|")
	for _, p := range cp {
		fmt.Fprintf(&b, "| %s | %s |\n", p.Name, p.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderClamps emits the hard-clamp ranges in the same notation the spec
// uses (∈ [min, max]). Stable order mirrors the spec table.
func renderClamps(c HardClamps) string {
	var b strings.Builder
	fmt.Fprintf(&b, "meta.max_esc      ∈ [%d, %d]\n", c.MaxEscMin, c.MaxEscMax)
	fmt.Fprintf(&b, "meta.max_agents   ∈ [%d, %d]\n", c.MaxAgentsMin, c.MaxAgentsMax)
	fmt.Fprintf(&b, "meta.max_local    ∈ [%d, %d]\n", c.MaxLocalMin, c.MaxLocalMax)
	fmt.Fprintf(&b, "meta.budget_usd   ∈ [%.1f, %.1f]\n", c.BudgetUSDMin, c.BudgetUSDMax)
	fmt.Fprintf(&b, "local.iter        ∈ [%d, %d]\n", c.LocalIterMin, c.LocalIterMax)
	fmt.Fprintf(&b, "agent.max_iter    ∈ [%d, %d]", c.AgentIterMin, c.AgentIterMax)
	return b.String()
}
