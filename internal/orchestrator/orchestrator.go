// Package orchestrator wires together the manifest, permissions, planner
// template, and executor surfaces that intern's --orchestrate=shadow mode
// uses. The package is the single entry point the proxy talks to: it
// hides the file paths under ~/.intern/, the planner Render call, and
// the executor lifecycle behind a small façade.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abhishekjha17/intern/internal/orchestrator/exec"
	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
	"github.com/abhishekjha17/intern/internal/orchestrator/planner"
	"github.com/abhishekjha17/intern/internal/orchestrator/tools"
)

// DefaultConfigDir is the path under which intern reads the manifest and
// permissions YAML files. Override-able for tests via NewWithPaths.
func DefaultConfigDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".intern")
	}
	return ".intern"
}

// Orchestrator bundles the parsed config and runtime registries. One per
// proxy process; safe for concurrent reads after construction.
type Orchestrator struct {
	Manifest    *manifest.Manifest
	Permissions *manifest.Permissions
	Tools       *tools.Registry

	// Cached planner block. Recomputed only when the manifest hash changes
	// (which it doesn't during a process lifetime — manifest is loaded
	// once at startup), so this is effectively constant after New.
	plannerBlock string

	// agentsDisabled, when true, causes bakePlannerBlock and
	// checkContext to behave as if no agent profiles exist. Set via
	// DisableAgents(); used when no AgentBackend is wired so plans
	// don't fruitlessly emit `agent` steps.
	agentsDisabled bool
}

// New loads config from DefaultConfigDir and returns an Orchestrator
// ready to serve. A missing config dir is not an error — the manifest
// has zero models and the permissions fall back to the safe defaults.
func New() (*Orchestrator, error) {
	dir := DefaultConfigDir()
	return NewWithPaths(
		filepath.Join(dir, "local_models.yaml"),
		filepath.Join(dir, "permissions.yaml"),
	)
}

// NewWithPaths is New with explicit file paths for the manifest and
// permissions YAMLs. Either may point to a non-existent file; the
// missing-file branch in each loader produces sensible defaults.
func NewWithPaths(manifestPath, permsPath string) (*Orchestrator, error) {
	m, err := manifest.LoadManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load manifest: %w", err)
	}
	perms, err := manifest.LoadPermissions(permsPath)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load permissions: %w", err)
	}
	o := &Orchestrator{
		Manifest:    m,
		Permissions: perms,
		Tools:       tools.NewRegistry(perms),
	}
	if err := o.bakePlannerBlock(planner.DefaultClientTools); err != nil {
		return nil, err
	}
	return o, nil
}

// PlannerBlock returns the rendered system-prompt block. It is byte-stable
// across calls within the same Orchestrator instance, so the proxy can
// safely splice it into outgoing requests with a consistent
// cache_control: ephemeral block boundary.
func (o *Orchestrator) PlannerBlock() string { return o.plannerBlock }

// DisableAgents re-bakes the planner block with an empty agent-profile
// list. Use when no AgentBackend is wired (current full-mode default):
// without this, the planner emits `agent <profile>` steps that the
// executor can't dispatch, causing every such plan to escalate. The
// rendered block instead tells the planner agent steps are unavailable
// so it routes work through `local` and `internal_tool` only. Also
// clears KnownAgentProfiles in the check context so any plan that
// somehow still references an agent step fails at check time.
func (o *Orchestrator) DisableAgents() error {
	o.agentsDisabled = true
	return o.bakePlannerBlock(planner.DefaultClientTools)
}

// EmitPlanTool returns the Anthropic tool definition that planning calls
// must include in their `tools` array. Its identity is stable so cached
// prefixes line up with the planner block above.
func (o *Orchestrator) EmitPlanTool() json.RawMessage { return planner.EmitPlanTool() }

// EmitPlanToolChoice returns the tool_choice JSON forcing the model to
// invoke emit_plan rather than reply free-form.
func (o *Orchestrator) EmitPlanToolChoice() json.RawMessage { return planner.EmitPlanToolChoice() }

// ExecutePlan parses src, runs the semantic checker against the
// orchestrator's known tools/models, and runs the executor. Returns the
// terminal message (if the plan ran cleanly), the executor result for
// downstream tracing, and any error.
//
// LocalBackend / AgentBackend / ClientBridge are not wired in this
// shadow-mode build; steps that need them fail with the matching
// Err*NotConfigured. Callers should pass a shadow logger and treat those
// errors as expected diagnostics rather than hard failures.
func (o *Orchestrator) ExecutePlan(ctx context.Context, src string) (string, *exec.Result, error) {
	p, err := plan.Parse(src)
	if err != nil {
		return "", nil, err
	}
	errs := plan.Check(p, o.checkContext())
	if len(errs) > 0 {
		plan.SetSource(errs, src)
		return "", nil, errs[0]
	}
	ex := &exec.Executor{Tools: o.Tools}
	res, err := ex.Run(ctx, p)
	if err != nil {
		return "", res, err
	}
	return res.Terminal, res, nil
}

// CheckPlan parses+validates without running. Used by callers that just
// want to sanity-check a plan source (e.g. an `intern plan-check` CLI).
func (o *Orchestrator) CheckPlan(src string) (*plan.Plan, []*plan.Error, error) {
	p, err := plan.Parse(src)
	if err != nil {
		return nil, nil, err
	}
	errs := plan.Check(p, o.checkContext())
	plan.SetSource(errs, src)
	return p, errs, nil
}

// checkContext returns the CheckContext the orchestrator uses to validate
// plans against the active manifest and permissions. The manifest hash
// is included so plans that drifted from a stale manifest are rejected.
func (o *Orchestrator) checkContext() plan.CheckContext {
	known := []string{}
	for _, mod := range o.Manifest.Models {
		known = append(known, mod.ID)
	}
	intern := o.Permissions.AllowedToolNames()

	clientTools := make([]string, 0, len(planner.DefaultClientTools))
	for _, t := range planner.DefaultClientTools {
		clientTools = append(clientTools, t.Name)
	}
	var profiles []string
	if !o.agentsDisabled {
		profiles = make([]string, 0, len(planner.DefaultAgentProfiles))
		for _, p := range planner.DefaultAgentProfiles {
			profiles = append(profiles, p.Name)
		}
	} else {
		// Empty (non-nil) slice so the check rejects any agent step,
		// rather than nil which the checker treats as "unknown,
		// don't enforce".
		profiles = []string{}
	}
	return plan.CheckContext{
		KnownModels:        known,
		KnownClientTools:   clientTools,
		KnownInternalTools: intern,
		KnownAgentProfiles: profiles,
		ExpectedManifest:   o.Manifest.Hash,
	}
}

// bakePlannerBlock renders the planner prompt against the orchestrator's
// loaded config. Called once during New; re-called by DisableAgents to
// strip agent profiles from the prompt when no AgentBackend is wired.
func (o *Orchestrator) bakePlannerBlock(clientTools []planner.ClientTool) error {
	profiles := planner.DefaultAgentProfiles
	if o.agentsDisabled {
		// Render's ctx.AgentProfiles == nil branch falls back to the
		// defaults, so we pass an explicit empty slice to actually
		// suppress the AGENT PROFILES table.
		profiles = []planner.AgentProfile{}
	}
	block, err := planner.Render(planner.Context{
		Manifest:      o.Manifest,
		Permissions:   o.Permissions,
		ClientTools:   clientTools,
		AgentProfiles: profiles,
		Clamps:        planner.DefaultHardClamps,
	})
	if err != nil {
		return fmt.Errorf("orchestrator: render planner: %w", err)
	}
	o.plannerBlock = block
	return nil
}
