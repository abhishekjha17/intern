package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWithPaths_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	o, err := NewWithPaths(
		filepath.Join(dir, "no-such-models.yaml"),
		filepath.Join(dir, "no-such-perms.yaml"),
	)
	if err != nil {
		t.Fatalf("NewWithPaths: %v", err)
	}
	if o.Manifest == nil || o.Permissions == nil {
		t.Fatal("expected non-nil manifest and permissions even with missing files")
	}
	if o.PlannerBlock() == "" {
		t.Error("expected non-empty planner block")
	}
	if !strings.Contains(o.PlannerBlock(), o.Manifest.Hash) {
		t.Error("planner block should include manifest hash")
	}
}

func TestPlannerBlock_StableAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewWithPaths(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"))
	b, _ := NewWithPaths(filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"))
	if a.PlannerBlock() != b.PlannerBlock() {
		t.Error("planner block not byte-stable across instances with same config")
	}
	if a.Manifest.Hash != b.Manifest.Hash {
		t.Error("manifest hash unstable")
	}
}

func TestDisableAgents_StripsAgentTableFromPlannerBlock(t *testing.T) {
	o, err := NewWithPaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(o.PlannerBlock(), "Explore") {
		t.Fatal("expected default planner block to advertise the Explore agent profile")
	}
	if err := o.DisableAgents(); err != nil {
		t.Fatalf("DisableAgents: %v", err)
	}
	if strings.Contains(o.PlannerBlock(), "Explore") {
		t.Error("planner block still advertises Explore profile after DisableAgents")
	}
	if !strings.Contains(o.PlannerBlock(), "agent` steps are unavailable") {
		t.Error("planner block should tell the planner that agent steps are unavailable")
	}
}

func TestDisableAgents_PlanReferencingAgentFailsCheck(t *testing.T) {
	o, err := NewWithPaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.DisableAgents(); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`plan @v1 {
		meta { entry: s1 manifest: %s }
		s1 := agent Explore "look around" -> $r => t
		t := terminal "${r}"
	}`, o.Manifest.Hash)
	_, errs, err := o.CheckPlan(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) == 0 {
		t.Fatal("expected check error for agent step with no profiles registered")
	}
	found := false
	for _, e := range errs {
		if e.Category == "unresolved_reference" && strings.Contains(e.Message, "Explore") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unresolved_reference for agent profile Explore; got: %v", errs)
	}
}

func TestExecutePlan_Terminal(t *testing.T) {
	o, err := NewWithPaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`plan @v1 {
		meta {
			entry: t
			manifest: %s
		}
		t := terminal "ok"
	}`, o.Manifest.Hash)
	msg, _, err := o.ExecutePlan(context.Background(), src)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if msg != "ok" {
		t.Errorf("terminal = %q", msg)
	}
}

func TestExecutePlan_InternalToolBash(t *testing.T) {
	o, err := NewWithPaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`plan @v1 {
		meta {
			entry: s1
			manifest: %s
		}
		s1 := internal_tool Bash {cmd: "echo hi"} -> $r => term
		term := terminal "${r.stdout}"
	}`, o.Manifest.Hash)
	msg, _, err := o.ExecutePlan(context.Background(), src)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if msg != "hi\n" {
		t.Errorf("terminal = %q", msg)
	}
}

func TestCheckPlan_RejectsManifestMismatch(t *testing.T) {
	o, _ := NewWithPaths("", "")
	src := `plan @v1 {
		meta {
			entry: t
			manifest: 0xdeadbeef
		}
		t := terminal "x"
	}`
	_, errs, err := o.CheckPlan(src)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range errs {
		if e.Category == "manifest_mismatch" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected manifest_mismatch diagnostic, got %d errs", len(errs))
		for _, e := range errs {
			t.Logf("  %s: %s", e.Category, e.Message)
		}
	}
}

// ---- Shadow hook -------------------------------------------------------

func TestShadowHook_LogsFirstOfSession(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	o, _ := NewWithPaths("", "")
	hook, err := NewShadowHook(o, logPath)
	if err != nil {
		t.Fatal(err)
	}

	// Two requests for the same session — only the first should be logged.
	hook.OnRequest("sess-1", "claude-opus-4-7", []byte(`{"messages":[]}`))
	hook.OnRequest("sess-1", "claude-opus-4-7", []byte(`{"messages":[]}`))
	// Different session — adds a second line.
	hook.OnRequest("sess-2", "claude-opus-4-7", []byte(`{"messages":[]}`))
	if err := hook.Close(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 shadow log lines, got %d:\n%s", len(lines), body)
	}

	for _, l := range lines {
		var entry struct {
			SessionID    string `json:"session_id"`
			Model        string `json:"model"`
			ManifestHash string `json:"manifest_hash"`
			PlannerBytes int    `json:"planner_bytes"`
		}
		if err := json.Unmarshal([]byte(l), &entry); err != nil {
			t.Fatalf("entry not JSON: %v\n%s", err, l)
		}
		if entry.PlannerBytes <= 0 {
			t.Errorf("planner_bytes <= 0 in %s", l)
		}
		if entry.ManifestHash != o.Manifest.Hash {
			t.Errorf("manifest hash drifted: %s vs %s", entry.ManifestHash, o.Manifest.Hash)
		}
	}
}

func TestShadowHook_EmptySessionIDSkipped(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shadow.jsonl")
	o, _ := NewWithPaths("", "")
	hook, _ := NewShadowHook(o, logPath)
	hook.OnRequest("", "claude-opus-4-7", nil)
	hook.Close()

	if body, _ := os.ReadFile(logPath); len(body) != 0 {
		t.Errorf("expected empty shadow log for empty session ID, got: %s", body)
	}
}

func TestShadowHook_NilSafe(t *testing.T) {
	var h *ShadowHook
	// Should not panic.
	h.OnRequest("any", "claude", nil)
	if err := h.Close(); err != nil {
		t.Errorf("Close on nil hook should be no-op, got %v", err)
	}
}
