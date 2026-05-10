package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
)

// BashInput is the JSON shape internal_tool Bash steps deserialize into.
type BashInput struct {
	Cmd        string `json:"cmd"`
	Cwd        string `json:"cwd,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
	StdoutCap  int    `json:"stdout_cap,omitempty"` // bytes; 0 → 1MiB default
}

const (
	defaultBashTimeoutMS = 30_000
	defaultBashStdoutCap = 1 << 20 // 1 MiB
)

// BashTool runs a shell command via `bash -c`, gated by the permissions'
// Bash allowlist. Returns *plan.ExecResult so predicates can read .stdout,
// .stderr, .exit field paths.
type BashTool struct{}

func (BashTool) Name() string { return "Bash" }

func (BashTool) Run(ctx context.Context, raw json.RawMessage, perms *manifest.Permissions) (any, error) {
	var in BashInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("Bash: bad input: %w", err)
	}
	if in.Cmd == "" {
		return nil, fmt.Errorf("Bash: empty cmd")
	}
	if perms != nil && !perms.AllowsBash(in.Cmd) {
		return nil, fmt.Errorf("Bash: command %q not in allowlist (first-token policy)", firstWord(in.Cmd))
	}

	timeout := in.TimeoutMS
	if timeout <= 0 {
		timeout = defaultBashTimeoutMS
	}
	cap := in.StdoutCap
	if cap <= 0 {
		cap = defaultBashStdoutCap
	}

	tctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(tctx, "bash", "-c", in.Cmd)
	if in.Cwd != "" {
		cmd.Dir = in.Cwd
	}
	var stdout, stderr capWriter
	stdout.cap = cap
	stderr.cap = cap
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if tctx.Err() == context.DeadlineExceeded {
			return &plan.ExecResult{
				Stdout: stdout.String(),
				Stderr: stderr.String() + "\n[bash: timed out]",
				Exit:   124,
			}, nil
		} else {
			return nil, fmt.Errorf("Bash: %w", err)
		}
	}
	return &plan.ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Exit:   exitCode,
	}, nil
}

// capWriter is a bytes.Buffer that drops writes after `cap` bytes. Prevents
// runaway commands (e.g. `find /`) from blowing up intern's memory.
type capWriter struct {
	buf bytes.Buffer
	cap int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len() >= w.cap {
		return len(p), nil
	}
	left := w.cap - w.buf.Len()
	if len(p) > left {
		w.buf.Write(p[:left])
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *capWriter) String() string { return w.buf.String() }

// firstWord is a lightweight peek at the first non-assignment token.
// Mirrors manifest.firstWord; duplicated here to keep the package tree free
// of tool→manifest internal coupling.
func firstWord(cmd string) string {
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if c == ' ' || c == '\t' {
			continue
		}
		// Skip env-style assignments at the front: KEY=VAL ... cmd ...
		eq := -1
		end := i
		for end < len(cmd) && cmd[end] != ' ' && cmd[end] != '\t' {
			if cmd[end] == '=' && eq < 0 {
				eq = end
			}
			end++
		}
		if eq > i {
			i = end - 1
			continue
		}
		return cmd[i:end]
	}
	return ""
}
