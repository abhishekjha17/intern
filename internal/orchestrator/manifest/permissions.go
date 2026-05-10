package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Permissions describes which tools intern is allowed to invoke internally
// (i.e. on behalf of a local model or a plan's `internal_tool` step) without
// any user-facing prompt. State-mutating ops should generally go through
// `client_tool` so the user sees and approves them; this allowlist scopes
// what intern itself is willing to execute.
type Permissions struct {
	// Internal lists tools intern may run inline. Each entry is a tool name
	// optionally constrained by a Bash subcommand allowlist.
	Internal []ToolPermission `yaml:"internal"`
}

// ToolPermission scopes a single tool. For Bash, BashAllowlist (when
// non-empty) restricts permitted commands to those whose first token (after
// shell parsing) appears in the list. An empty BashAllowlist means
// "unrestricted Bash" — by default we ship a read-only allowlist.
type ToolPermission struct {
	Name          string   `yaml:"name"`
	BashAllowlist []string `yaml:"bash_allowlist,omitempty"`
}

// LoadPermissions reads the allowlist YAML at path. A missing file yields
// the default safe allowlist below.
func LoadPermissions(path string) (*Permissions, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("permissions %s: %w", path, err)
	}
	if len(raw) == 0 {
		return DefaultPermissions(), nil
	}
	p := &Permissions{}
	if err := yaml.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("permissions %s: %w", path, err)
	}
	return p, nil
}

// DefaultPermissions returns the safe shipped defaults: Read, Grep, Glob
// fully permitted, and Bash restricted to a read-only command set.
func DefaultPermissions() *Permissions {
	return &Permissions{
		Internal: []ToolPermission{
			{Name: "Read"},
			{Name: "Grep"},
			{Name: "Glob"},
			{
				Name: "Bash",
				BashAllowlist: []string{
					"ls", "cat", "git", "grep", "find", "head", "tail", "wc",
					"echo", "test", "stat", "file", "pwd", "tree", "diff",
				},
			},
		},
	}
}

// AllowedToolNames returns the set of internal tool names allowed.
func (p *Permissions) AllowedToolNames() []string {
	out := make([]string, 0, len(p.Internal))
	for _, t := range p.Internal {
		out = append(out, t.Name)
	}
	return out
}

// AllowsBash returns true if cmd's first token is in the Bash allowlist (or
// the allowlist is unrestricted/empty for Bash). cmd is a raw shell command
// string; we inspect only its first whitespace-delimited token.
func (p *Permissions) AllowsBash(cmd string) bool {
	for _, t := range p.Internal {
		if t.Name != "Bash" {
			continue
		}
		if len(t.BashAllowlist) == 0 {
			return true
		}
		first := firstWord(cmd)
		for _, allowed := range t.BashAllowlist {
			if first == allowed {
				return true
			}
		}
		return false
	}
	return false
}

func firstWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	// Skip env-style prefixes (FOO=bar cmd). We only look at the first
	// non-assignment token.
	for {
		i := strings.IndexAny(cmd, " \t")
		if i < 0 {
			i = len(cmd)
		}
		tok := cmd[:i]
		if !strings.Contains(tok, "=") || strings.HasPrefix(tok, "=") {
			return tok
		}
		// env-assignment; advance past it
		if i >= len(cmd) {
			return ""
		}
		cmd = strings.TrimLeft(cmd[i:], " \t")
	}
}
