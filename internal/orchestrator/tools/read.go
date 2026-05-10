package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

// ReadInput mirrors the Anthropic Read tool's argument shape.
type ReadInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"` // 1-indexed line offset
	Limit  int    `json:"limit,omitempty"`  // line count cap; 0 → 2000 default
}

const (
	defaultReadLimit = 2000
	maxReadBytes     = 5 << 20 // 5 MiB safety cap
)

// ReadTool reads a file, optionally returning a line range. Output is the
// file content as a string with `cat -n` style line numbers, matching the
// shape of Claude Code's Read so downstream prompts are familiar.
type ReadTool struct{}

func (ReadTool) Name() string { return "Read" }

func (ReadTool) Run(ctx context.Context, raw json.RawMessage, _ *manifest.Permissions) (any, error) {
	var in ReadInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("Read: bad input: %w", err)
	}
	if in.Path == "" {
		return nil, fmt.Errorf("Read: empty path")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultReadLimit
	}

	st, err := os.Stat(in.Path)
	if err != nil {
		return nil, fmt.Errorf("Read: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("Read: %s is a directory", in.Path)
	}
	if st.Size() > maxReadBytes {
		return nil, fmt.Errorf("Read: %s exceeds %d byte cap", in.Path, maxReadBytes)
	}

	f, err := os.Open(in.Path)
	if err != nil {
		return nil, fmt.Errorf("Read: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var b strings.Builder
	lineNo := 0
	emitted := 0
	startLine := in.Offset
	if startLine <= 0 {
		startLine = 1
	}
	for scanner.Scan() {
		lineNo++
		if lineNo < startLine {
			continue
		}
		if emitted >= limit {
			break
		}
		fmt.Fprintf(&b, "%6d\t%s\n", lineNo, scanner.Text())
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("Read: %w", err)
	}
	return b.String(), nil
}
