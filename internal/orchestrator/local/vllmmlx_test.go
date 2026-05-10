package local

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/exec"
	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

// fakeServer returns a tiny httptest.Server that mimics vllm-mlx's
// /v1/chat/completions endpoint. responseText is echoed verbatim into
// the choices[0].message.content; capture (if non-nil) records the
// last received request body so tests can assert what we sent.
func fakeServer(t *testing.T, responseText string, capture *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if capture != nil {
			_ = json.Unmarshal(body, capture)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` +
			mustJSONString(responseText) + `},"finish_reason":"stop"}]}`))
	}))
}

func mustJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRunLocal_HappyPath(t *testing.T) {
	srv := fakeServer(t, "summary text", nil)
	defer srv.Close()

	m := &manifest.Manifest{Models: []manifest.Model{{
		ID:       "qwen3-1_7b",
		Backend:  "vllm-mlx",
		Endpoint: srv.URL,
		Model:    "vendor/Qwen3-1.7B-MLX-4bit",
	}}}
	b := NewVLLMMLX(m)

	out, err := b.RunLocal(context.Background(), exec.LocalRequest{
		ModelID: "qwen3-1_7b",
		Prompt:  "summarize this",
		Iter:    3,
		Timeout: 5 * time.Second,
		Format:  "text",
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if out.Output != "summary text" {
		t.Errorf("output = %q, want %q", out.Output, "summary text")
	}
}

func TestRunLocal_StripsThinkingBlock(t *testing.T) {
	srv := fakeServer(t, "<think>reasoning here</think>\n\nactual answer", nil)
	defer srv.Close()

	m := &manifest.Manifest{Models: []manifest.Model{{
		ID:       "qwen3-1_7b",
		Endpoint: srv.URL,
		Model:    "vendor/x",
	}}}
	b := NewVLLMMLX(m)

	out, _ := b.RunLocal(context.Background(), exec.LocalRequest{
		ModelID: "qwen3-1_7b", Prompt: "p",
	})
	got := out.Output.(string)
	if !strings.HasPrefix(got, "actual answer") {
		t.Errorf("expected stripped, got %q", got)
	}
}

func TestRunLocal_SendsExtraParams(t *testing.T) {
	captured := map[string]any{}
	srv := fakeServer(t, "ok", &captured)
	defer srv.Close()

	m := &manifest.Manifest{Models: []manifest.Model{{
		ID:       "qwen3-1_7b",
		Endpoint: srv.URL,
		Model:    "vendor/x",
		ExtraRequestParams: map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": false},
		},
	}}}
	b := NewVLLMMLX(m)

	if _, err := b.RunLocal(context.Background(), exec.LocalRequest{
		ModelID: "qwen3-1_7b", Prompt: "p",
	}); err != nil {
		t.Fatal(err)
	}

	cw, _ := captured["chat_template_kwargs"].(map[string]any)
	if v, _ := cw["enable_thinking"].(bool); v != false {
		t.Errorf("expected chat_template_kwargs.enable_thinking=false, got %+v", captured["chat_template_kwargs"])
	}
}

func TestRunLocal_FormatJSONDecodes(t *testing.T) {
	srv := fakeServer(t, `{"key": "value", "n": 7}`, nil)
	defer srv.Close()

	m := &manifest.Manifest{Models: []manifest.Model{{
		ID:       "m",
		Endpoint: srv.URL,
		Model:    "x",
	}}}
	b := NewVLLMMLX(m)

	out, err := b.RunLocal(context.Background(), exec.LocalRequest{
		ModelID: "m", Prompt: "p", Format: "json",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T: %v", out.Output, out.Output)
	}
	if got["key"] != "value" {
		t.Errorf("output[key] = %v", got["key"])
	}
}

func TestRunLocal_UnknownModelErrs(t *testing.T) {
	b := NewVLLMMLX(&manifest.Manifest{})
	_, err := b.RunLocal(context.Background(), exec.LocalRequest{ModelID: "ghost"})
	if err == nil {
		t.Fatal("expected error for unknown model_id")
	}
}

func TestRunLocal_ToolsRejectedInV0(t *testing.T) {
	m := &manifest.Manifest{Models: []manifest.Model{{ID: "m", Endpoint: "http://localhost:1", Model: "x"}}}
	b := NewVLLMMLX(m)
	_, err := b.RunLocal(context.Background(), exec.LocalRequest{
		ModelID: "m", Prompt: "p", Tools: []string{"Read"},
	})
	if err == nil {
		t.Fatal("expected error when tools=[...] is set; v0 doesn't run the local agent loop")
	}
}
