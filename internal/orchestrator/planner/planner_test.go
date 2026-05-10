package planner

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
	"github.com/abhishekjha17/intern/internal/orchestrator/plan"
)

// fixtureManifest returns a manifest with two models in deliberately-non-id-order
// so we can verify Render sorts before emitting.
func fixtureManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m := &manifest.Manifest{
		Models: []manifest.Model{
			{
				ID:               "llama3-8b",
				Capabilities:     []string{"summarize", "classify"},
				AntiCapabilities: []string{"code_generation_multi_file"},
				MaxInputTokens:   8000,
				AvgLatencyMS:     800,
				Description:      "General 8B.",
			},
			{
				ID:               "granite-3b",
				Capabilities:     []string{"route_decision"},
				AntiCapabilities: []string{"summarize_long"},
				MaxInputTokens:   4000,
				AvgLatencyMS:     250,
				Description:      "Fast 3B.",
			},
		},
	}
	m.Hash = "0xdeadbeefcafebabe"
	return m
}

func TestRender_AllPlaceholdersExpanded(t *testing.T) {
	m := fixtureManifest(t)
	p := manifest.DefaultPermissions()
	out, err := Render(Context{Manifest: m, Permissions: p})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(out, "{{") {
		t.Errorf("template still contains {{...}} placeholder:\n%s",
			previewLineContaining(out, "{{"))
	}
	for _, want := range []string{
		"0xdeadbeefcafebabe",   // manifest hash
		"granite-3b",           // model id
		"llama3-8b",            // model id
		"AskUserQuestion",      // default client tool
		"code-reviewer",        // default agent profile
		"meta.max_esc",         // hard clamps
		"unrestricted shell",   // BashAllowlist=nil branch... actually default has allowlist
		"allowlist:",           // default permissions has Bash with allowlist
	} {
		if want == "unrestricted shell" {
			continue // we use default permissions; allowlist is non-empty
		}
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRender_StableBytesAcrossRuns(t *testing.T) {
	// Render twice with the same context; output must be byte-identical.
	m := fixtureManifest(t)
	p := manifest.DefaultPermissions()
	a, _ := Render(Context{Manifest: m, Permissions: p})
	b, _ := Render(Context{Manifest: m, Permissions: p})
	if a != b {
		t.Errorf("Render is non-deterministic")
	}
}

func TestRender_SortsModelsByID(t *testing.T) {
	m := fixtureManifest(t)
	p := manifest.DefaultPermissions()
	out, _ := Render(Context{Manifest: m, Permissions: p})
	graniteIdx := strings.Index(out, "granite-3b")
	llamaIdx := strings.Index(out, "llama3-8b")
	if graniteIdx == -1 || llamaIdx == -1 {
		t.Fatalf("missing model rows: granite=%d llama=%d", graniteIdx, llamaIdx)
	}
	if graniteIdx >= llamaIdx {
		t.Errorf("expected granite-3b before llama3-8b alphabetically; got granite@%d llama@%d", graniteIdx, llamaIdx)
	}
}

func TestRender_EmptyManifestUsesFallback(t *testing.T) {
	m := &manifest.Manifest{Hash: "0x0"}
	p := manifest.DefaultPermissions()
	out, err := Render(Context{Manifest: m, Permissions: p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No local models configured") {
		t.Error("expected fallback message when manifest has no models")
	}
}

func TestRender_RejectsNilDeps(t *testing.T) {
	if _, err := Render(Context{Permissions: manifest.DefaultPermissions()}); err == nil {
		t.Error("expected error when manifest is nil")
	}
	if _, err := Render(Context{Manifest: &manifest.Manifest{}}); err == nil {
		t.Error("expected error when permissions is nil")
	}
}

// previewLineContaining returns the line in s that contains needle, for
// readable test failure output.
func previewLineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// ---- Examples in the template parse and check cleanly -----------------

// TestRender_WorkedExamplesParseAndCheck makes sure the worked examples
// embedded in the prompt are valid plans the parser+checker accept (with
// the manifest_hash from this fixture). This guards against drift between
// the doc/examples and the grammar implementation.
func TestRender_WorkedExamplesParseAndCheck(t *testing.T) {
	m := fixtureManifest(t)
	p := manifest.DefaultPermissions()
	out, err := Render(Context{Manifest: m, Permissions: p})
	if err != nil {
		t.Fatal(err)
	}

	for i, ex := range extractExamples(out) {
		ex := ex
		t.Run(ex.title, func(t *testing.T) {
			parsed, perr := plan.Parse(ex.body)
			if perr != nil {
				t.Fatalf("example %d %q parse: %v", i, ex.title, perr)
			}
			errs := plan.Check(parsed, plan.CheckContext{
				ExpectedManifest: m.Hash,
			})
			if len(errs) > 0 {
				plan.SetSource(errs, ex.body)
				for _, e := range errs {
					t.Errorf("%v", e)
				}
			}
		})
	}
}

type example struct {
	title string
	body  string
}

// extractExamples pulls each `plan @v1 { ... }` block out of the rendered
// template along with its preceding `# Example X — ...` comment as the
// title. Only matches `plan @v1 {` at the start of a line, so the prose
// reference inside the closing instruction text doesn't get picked up.
// Brace-balancing handles nested braces in obj/meta literals.
func extractExamples(s string) []example {
	var out []example
	idx := 0
	for {
		rel := strings.Index(s[idx:], "\nplan @v1 {")
		if rel < 0 {
			break
		}
		start := idx + rel + 1
		// Pull the title from the most recent "# Example" line before start.
		title := "unknown"
		if i := strings.LastIndex(s[:start], "\n# Example"); i >= 0 {
			lineStart := i + 1
			lineEnd := strings.IndexByte(s[lineStart:], '\n')
			if lineEnd > 0 {
				title = strings.TrimPrefix(s[lineStart:lineStart+lineEnd], "# ")
			}
		}
		// Brace-match from the first `{` of `plan @v1 {`.
		brace := strings.IndexByte(s[start:], '{')
		if brace < 0 {
			break
		}
		i := start + brace
		depth := 0
		end := -1
		for ; i < len(s); i++ {
			switch s[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i + 1
				}
			case '"':
				j := i + 1
				for j < len(s) && s[j] != '"' {
					if s[j] == '\\' && j+1 < len(s) {
						j += 2
						continue
					}
					j++
				}
				i = j
			}
			if end > 0 {
				break
			}
		}
		if end < 0 {
			break
		}
		out = append(out, example{title: title, body: s[start:end]})
		idx = end
	}
	return out
}

// ---- emit_plan tool ---------------------------------------------------

func TestEmitPlanTool_DefinitionShape(t *testing.T) {
	def := EmitPlanTool()
	var got struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Type     string `json:"type"`
			Required []string
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(def, &got); err != nil {
		t.Fatalf("tool def not JSON: %v", err)
	}
	if got.Name != "emit_plan" {
		t.Errorf("name = %q", got.Name)
	}
	if got.InputSchema.Type != "object" {
		t.Errorf("input schema type = %q", got.InputSchema.Type)
	}
}

func TestEmitPlan_Extract_HappyPath(t *testing.T) {
	body := []byte(`{
		"role": "assistant",
		"content": [
			{"type": "text", "text": "thinking..."},
			{"type": "tool_use", "name": "emit_plan", "id": "tu_1",
			 "input": {"program": "plan @v1 { meta { entry: t } t := terminal \"hi\" }",
			           "rationale": "trivial"}}
		]
	}`)
	in, err := ExtractFromMessageJSON(body)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(in.Program, "plan @v1") {
		t.Errorf("program = %q", in.Program)
	}
	if in.Rationale != "trivial" {
		t.Errorf("rationale = %q", in.Rationale)
	}
}

func TestEmitPlan_Extract_NoToolUse(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"sorry"}]}`)
	_, err := ExtractFromMessageJSON(body)
	if !errors.Is(err, ErrNoEmitPlan) {
		t.Errorf("expected ErrNoEmitPlan, got %v", err)
	}
}

func TestEmitPlan_Extract_OtherToolNameIgnored(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","name":"some_other","input":{"x":1}}]}`)
	_, err := ExtractFromMessageJSON(body)
	if !errors.Is(err, ErrNoEmitPlan) {
		t.Errorf("expected ErrNoEmitPlan, got %v", err)
	}
}

func TestEmitPlan_Extract_EmptyProgramRejected(t *testing.T) {
	body := []byte(`{"content":[{"type":"tool_use","name":"emit_plan","input":{"program":""}}]}`)
	_, err := ExtractFromMessageJSON(body)
	if err == nil || errors.Is(err, ErrNoEmitPlan) {
		t.Errorf("expected error for empty program, got %v", err)
	}
}
