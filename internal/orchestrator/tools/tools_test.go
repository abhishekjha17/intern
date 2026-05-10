package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
)

func TestRegistry_Dispatch(t *testing.T) {
	r := NewRegistry(manifest.DefaultPermissions())
	if _, ok := r.Get("Bash"); !ok {
		t.Error("Bash should be registered")
	}
	if _, ok := r.Get("Frobnicate"); ok {
		t.Error("unknown tool should not exist")
	}
}

func TestBash_AllowedCommand(t *testing.T) {
	r := NewRegistry(manifest.DefaultPermissions())
	out, err := r.Run(context.Background(), "Bash",
		json.RawMessage(`{"cmd":"echo hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*plan.ExecResult)
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("stdout=%q", res.Stdout)
	}
	if res.Exit != 0 {
		t.Errorf("exit=%d", res.Exit)
	}
}

func TestBash_DeniedCommand(t *testing.T) {
	r := NewRegistry(manifest.DefaultPermissions())
	_, err := r.Run(context.Background(), "Bash",
		json.RawMessage(`{"cmd":"rm -rf /"}`))
	if err == nil {
		t.Fatal("expected denial for 'rm'")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("expected allowlist error, got %v", err)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	r := NewRegistry(manifest.DefaultPermissions())
	out, err := r.Run(context.Background(), "Bash",
		json.RawMessage(`{"cmd":"test -f /no/such/file"}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*plan.ExecResult)
	if res.Exit == 0 {
		t.Errorf("expected non-zero exit, got %d", res.Exit)
	}
}

func TestRead_BasicLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644)

	r := NewRegistry(manifest.DefaultPermissions())
	out, err := r.Run(context.Background(), "Read",
		json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	s := out.(string)
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "gamma") {
		t.Errorf("read output wrong: %q", s)
	}
	if !strings.Contains(s, "     1\talpha") {
		t.Errorf("read should produce cat -n style line numbers; got %q", s)
	}
}

func TestRead_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644)

	r := NewRegistry(manifest.DefaultPermissions())
	out, _ := r.Run(context.Background(), "Read",
		json.RawMessage(`{"path":"`+path+`","offset":2,"limit":2}`))
	s := out.(string)
	if strings.Contains(s, "one") {
		t.Errorf("offset=2 should skip first line, got: %q", s)
	}
	if !strings.Contains(s, "two") || !strings.Contains(s, "three") {
		t.Errorf("expected two,three got: %q", s)
	}
	if strings.Contains(s, "four") {
		t.Errorf("limit=2 should cut after two lines, got: %q", s)
	}
}

func TestRead_DirectoryIsError(t *testing.T) {
	r := NewRegistry(manifest.DefaultPermissions())
	_, err := r.Run(context.Background(), "Read",
		json.RawMessage(`{"path":"`+os.TempDir()+`"}`))
	if err == nil {
		t.Fatal("expected error reading a directory")
	}
}

func TestGrep_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nfunc Foo()\nfunc Bar()\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package y\nfunc Baz()\n"), 0o644)

	r := NewRegistry(manifest.DefaultPermissions())
	out, err := r.Run(context.Background(), "Grep",
		json.RawMessage(`{"pattern":"^func ","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	hits := out.([]GrepHit)
	if len(hits) != 3 {
		t.Errorf("expected 3 hits, got %d: %+v", len(hits), hits)
	}
}

func TestGlob_Basic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "y.go"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(dir, "z.txt"), []byte("c"), 0o644)

	r := NewRegistry(manifest.DefaultPermissions())
	out, err := r.Run(context.Background(), "Glob",
		json.RawMessage(`{"pattern":"*.go","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := out.([]string)
	if len(got) != 2 {
		t.Errorf("expected 2 .go files, got %v", got)
	}
}

func TestRegistry_DenyUnregistered(t *testing.T) {
	// Only tools in perms.AllowedToolNames are runnable; Bash is omitted here.
	custom := &manifest.Permissions{
		Internal: []manifest.ToolPermission{{Name: "Read"}},
	}
	r := NewRegistry(custom)
	_, err := r.Run(context.Background(), "Bash",
		json.RawMessage(`{"cmd":"echo hi"}`))
	if err == nil {
		t.Fatal("expected denial for tool not in custom permissions")
	}
}
