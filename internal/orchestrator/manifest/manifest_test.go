package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempYAML(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadManifest_Empty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(m.Models) != 0 {
		t.Errorf("models=%d, want 0", len(m.Models))
	}
	if m.Hash == "" {
		t.Error("hash should be non-empty even with no models")
	}
}

func TestLoadManifest_Basic(t *testing.T) {
	path := writeTempYAML(t, "models.yaml", `
models:
  - id: llama3-8b
    backend: ollama
    endpoint: http://localhost:11434
    model: llama3.1:8b
    capabilities: [summarize, classify]
    anti_capabilities: [code_generation_multi_file]
    max_input_tokens: 8000
    avg_latency_ms: 800
    description: "general 8B"
    supports_tools: true
  - id: granite-3b
    backend: ollama
    model: granite:3b
    capabilities: [route_decision]
    max_input_tokens: 4000
    description: "fast routing"
`)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(m.Models) != 2 {
		t.Fatalf("models=%d", len(m.Models))
	}
	if mod := m.FindModel("llama3-8b"); mod == nil || !mod.SupportsTools {
		t.Errorf("FindModel llama3-8b = %+v", mod)
	}
	if m.FindModel("nope") != nil {
		t.Error("FindModel should return nil for unknown")
	}
}

func TestLoadManifest_HashStableAcrossOrder(t *testing.T) {
	src1 := `
models:
  - id: a
    backend: ollama
    model: a-tag
  - id: b
    backend: ollama
    model: b-tag
`
	src2 := `
models:
  - id: b
    backend: ollama
    model: b-tag
  - id: a
    backend: ollama
    model: a-tag
`
	m1, _ := LoadManifest(writeTempYAML(t, "1.yaml", src1))
	m2, _ := LoadManifest(writeTempYAML(t, "2.yaml", src2))
	if m1.Hash != m2.Hash {
		t.Errorf("expected stable hash across model order; got %s vs %s", m1.Hash, m2.Hash)
	}
}

func TestLoadManifest_HashChangesOnContentChange(t *testing.T) {
	src1 := `
models:
  - id: a
    backend: ollama
    model: tag1
`
	src2 := `
models:
  - id: a
    backend: ollama
    model: tag2
`
	m1, _ := LoadManifest(writeTempYAML(t, "1.yaml", src1))
	m2, _ := LoadManifest(writeTempYAML(t, "2.yaml", src2))
	if m1.Hash == m2.Hash {
		t.Errorf("expected hash to change with model content")
	}
}

func TestPermissions_DefaultAllowsReadOnlyBash(t *testing.T) {
	p := DefaultPermissions()
	if !p.AllowsBash("ls -la") {
		t.Error("default should allow 'ls'")
	}
	if !p.AllowsBash("git status") {
		t.Error("default should allow 'git'")
	}
	if p.AllowsBash("rm -rf /") {
		t.Error("default should NOT allow 'rm'")
	}
	if p.AllowsBash("curl http://...") {
		t.Error("default should NOT allow 'curl'")
	}
}

func TestPermissions_LoadCustom(t *testing.T) {
	path := writeTempYAML(t, "perms.yaml", `
internal:
  - name: Read
  - name: Bash
    bash_allowlist:
      - go
      - make
`)
	p, err := LoadPermissions(path)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !p.AllowsBash("go test ./...") {
		t.Error("expected go to be allowed")
	}
	if p.AllowsBash("ls") {
		t.Error("ls should NOT be allowed under custom allowlist")
	}
}

func TestPermissions_LoadMissingUsesDefaults(t *testing.T) {
	p, err := LoadPermissions(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	names := p.AllowedToolNames()
	if len(names) == 0 {
		t.Error("expected default allowlist for missing file")
	}
}

func TestPermissions_BashEnvAssignmentSkipped(t *testing.T) {
	p := DefaultPermissions()
	if !p.AllowsBash("FOO=bar git status") {
		t.Error("env-prefixed git should still be allowed")
	}
}
