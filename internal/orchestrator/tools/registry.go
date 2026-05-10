// Package tools implements the tool surfaces intern executes on behalf of
// local-model agent loops and `internal_tool` plan steps.
//
// Tools here run inside the intern process; their effects are not visible
// to the user's IDE. State-mutating operations should generally be routed
// through `client_tool` (fabricated tool_use blocks back to Claude Code) so
// the user can witness and approve them; the tools in this package default
// to a read-only allowlist that's safe to run silently.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

// Tool is the common interface for every internal tool. Run takes JSON-shaped
// input (matching the Anthropic-style tool input schema) and returns a value
// the orchestrator will bind to the step's output variable.
type Tool interface {
	Name() string
	Run(ctx context.Context, input json.RawMessage, perms *manifest.Permissions) (any, error)
}

// Registry holds the set of installed tools and the permissions they run
// under. It is safe for concurrent use after construction.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	perms *manifest.Permissions
}

// NewRegistry returns a registry with the default tool set wired up.
func NewRegistry(perms *manifest.Permissions) *Registry {
	r := &Registry{
		tools: map[string]Tool{},
		perms: perms,
	}
	r.Register(&BashTool{})
	r.Register(&ReadTool{})
	r.Register(&GrepTool{})
	r.Register(&GlobTool{})
	return r
}

// Register installs a tool. Existing tools with the same name are replaced.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// Get returns the tool by name and whether it exists.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the set of registered tool names. The slice is freshly
// allocated and not held by the registry.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	return out
}

// Run dispatches to the named tool with permission checking. The returned
// value is the bound output (e.g. *plan.ExecResult for Bash, string for
// Read, []string for Glob).
func (r *Registry) Run(ctx context.Context, name string, input json.RawMessage) (any, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if r.perms != nil {
		if !r.toolAllowed(name) {
			return nil, fmt.Errorf("tool %q not in internal permission allowlist", name)
		}
	}
	return t.Run(ctx, input, r.perms)
}

func (r *Registry) toolAllowed(name string) bool {
	for _, n := range r.perms.AllowedToolNames() {
		if n == name {
			return true
		}
	}
	return false
}
