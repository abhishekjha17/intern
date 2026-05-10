package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

// ---- Grep ---------------------------------------------------------------

// GrepInput mirrors a subset of Anthropic's Grep tool. We support pattern,
// path (file or directory), glob filter, and case-insensitive flag.
type GrepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

// GrepHit is one match.
type GrepHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

const defaultGrepMaxResults = 500

type GrepTool struct{}

func (GrepTool) Name() string { return "Grep" }

func (GrepTool) Run(ctx context.Context, raw json.RawMessage, _ *manifest.Permissions) (any, error) {
	var in GrepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("Grep: bad input: %w", err)
	}
	if in.Pattern == "" {
		return nil, fmt.Errorf("Grep: empty pattern")
	}
	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("Grep: invalid pattern: %w", err)
	}
	root := in.Path
	if root == "" {
		root = "."
	}
	max := in.MaxResults
	if max <= 0 {
		max = defaultGrepMaxResults
	}

	var hits []GrepHit
	walk := func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return nil // skip unreadable files silently
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			if re.MatchString(scanner.Text()) {
				hits = append(hits, GrepHit{Path: path, Line: lineNo, Text: scanner.Text()})
				if len(hits) >= max {
					return errStop
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return nil
	}

	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("Grep: %w", err)
	}
	if !st.IsDir() {
		_ = walk(root)
	} else {
		err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if in.Glob != "" {
				ok, _ := filepath.Match(in.Glob, filepath.Base(p))
				if !ok {
					return nil
				}
			}
			return walk(p)
		})
		if err != nil && err != errStop {
			return nil, fmt.Errorf("Grep: %w", err)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Line < hits[j].Line
	})
	return hits, nil
}

var errStop = fmt.Errorf("stop")

// ---- Glob ---------------------------------------------------------------

// GlobInput mirrors Anthropic's Glob tool.
type GlobInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type GlobTool struct{}

func (GlobTool) Name() string { return "Glob" }

func (GlobTool) Run(ctx context.Context, raw json.RawMessage, _ *manifest.Permissions) (any, error) {
	var in GlobInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("Glob: bad input: %w", err)
	}
	if in.Pattern == "" {
		return nil, fmt.Errorf("Glob: empty pattern")
	}
	root := in.Path
	if root == "" {
		root = "."
	}

	// Support ** in patterns by converting to a regex over the path tail.
	useDoubleStar := strings.Contains(in.Pattern, "**")

	var matches []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if useDoubleStar {
			if doubleStarMatch(in.Pattern, p) {
				matches = append(matches, p)
			}
			return nil
		}
		ok, _ := filepath.Match(in.Pattern, filepath.Base(p))
		if ok {
			matches = append(matches, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Glob: %w", err)
	}
	sort.Strings(matches)
	return matches, nil
}

// doubleStarMatch is a minimal `**` matcher: `**/foo` matches any path whose
// basename matches `foo`; `dir/**/*.go` matches `.go` files at any depth
// under `dir/`. Adequate for v1 — full doublestar semantics can come later
// via a dedicated lib if needed.
func doubleStarMatch(pat, path string) bool {
	if !strings.Contains(pat, "**") {
		ok, _ := filepath.Match(pat, path)
		return ok
	}
	parts := strings.Split(pat, "**")
	// Sequentially require each segment to appear in order.
	cur := path
	for i, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		if i == 0 {
			if !strings.HasPrefix(cur, part) {
				return false
			}
			cur = cur[len(part):]
			continue
		}
		if i == len(parts)-1 {
			// Last part may be a glob (e.g. `*.go`) — match against basename.
			ok, _ := filepath.Match(part, filepath.Base(path))
			return ok
		}
		idx := strings.Index(cur, part)
		if idx < 0 {
			return false
		}
		cur = cur[idx+len(part):]
	}
	return true
}
