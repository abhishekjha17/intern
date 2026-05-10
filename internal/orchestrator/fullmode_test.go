package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/abhishekjha17/intern/internal/orchestrator/exec"
)

// fakeAnthropic spins up a httptest.Server that mimics Anthropic's
// /v1/messages endpoint, returning a fixed JSON body. Capture (if
// non-nil) records the last received request body so tests can assert
// the planner block was injected and tool_choice was forced.
func fakeAnthropic(t *testing.T, respBody string, capture *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = body
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
}

// roundTripperFunc adapts a function into an http.RoundTripper for the
// FullModeTransport.Inner field — we plug a fake upstream this way.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// upstreamTo returns a RoundTripper that forwards to the given test
// server URL by replacing the request URL host/scheme. This is how we
// adapt a httptest.Server (with arbitrary scheme/host) into the
// transport's Inner without an actual reverse proxy.
func upstreamTo(srv *httptest.Server) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = srv.Listener.Addr().String()
		req.Host = req.URL.Host
		return http.DefaultTransport.RoundTrip(req)
	})
}

// orchForTest builds an orchestrator with no models (uses defaults).
func orchForTest(t *testing.T) *Orchestrator {
	t.Helper()
	o, err := NewWithPaths("/nonexistent/models.yaml", "/nonexistent/perms.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// orchWithModel builds an orchestrator whose manifest declares a single
// model so plans can reference it as `local <id>`. Used by tests that
// need the executor to dispatch to a LocalBackend.
func orchWithModel(t *testing.T, id string) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	yaml := []byte("models:\n" +
		"  - id: " + id + "\n" +
		"    backend: vllm-mlx\n" +
		"    endpoint: http://localhost:9999\n" +
		"    model: vendor/" + id + "\n" +
		"    capabilities: [summarize]\n" +
		"    max_input_tokens: 4096\n" +
		"    max_output_tokens: 1024\n" +
		"    avg_latency_ms: 200\n")
	path := dir + "/models.yaml"
	if err := os.WriteFile(path, yaml, 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := NewWithPaths(path, "/nonexistent/perms.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// canned emit_plan response for a trivial terminal-only plan.
func cannedEmitPlanResponse(t *testing.T, o *Orchestrator) string {
	t.Helper()
	program := fmt.Sprintf(`plan @v1 {
		meta {
			entry: t
			manifest: %s
		}
		t := terminal "executor ran successfully"
	}`, o.Manifest.Hash)
	resp := map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"role":  "assistant",
		"model": "claude-opus-4-7",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    "tu_1",
				"name":  "emit_plan",
				"input": map[string]any{"program": program, "rationale": "trivial"},
			},
		},
		"stop_reason": "tool_use",
		"usage":       map[string]int{"input_tokens": 1500, "output_tokens": 80},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// ---- Tests ------------------------------------------------------------

func TestFullMode_HappyPath(t *testing.T) {
	o := orchForTest(t)
	var captured []byte
	srv := fakeAnthropic(t, cannedEmitPlanResponse(t, o), &captured)
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)

	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	// Synthesized response should carry the executor's terminal text.
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	content, _ := got["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in synthesized response: %s", body)
	}
	first, _ := content[0].(map[string]any)
	if first["text"] != "executor ran successfully" {
		t.Errorf("terminal text not in synthesized response: %v", first)
	}

	// Verify what we sent upstream: planner block injected + emit_plan tool forced.
	var sent map[string]any
	_ = json.Unmarshal(captured, &sent)
	if _, ok := sent["tool_choice"]; !ok {
		t.Error("upstream request missing tool_choice")
	}
	tools, _ := sent["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("expected exactly 1 tool (emit_plan), got %d", len(tools))
	}
	stream, _ := sent["stream"].(bool)
	if stream {
		t.Error("upstream request should have stream=false")
	}
	system, _ := sent["system"].([]any)
	if len(system) == 0 {
		t.Error("system block missing from upstream request")
	}
}

func TestFullMode_StreamingSynthesizesSSE(t *testing.T) {
	// Streaming first-of-session requests are intercepted: the upstream
	// planning call is non-streaming (we need a complete plan), but the
	// response back to the client is a synthesized Anthropic-shaped SSE
	// stream so streaming SDK clients (Claude Code, Cursor) can consume it.
	o := orchForTest(t)
	var captured []byte
	srv := fakeAnthropic(t, cannedEmitPlanResponse(t, o), &captured)
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)

	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	// The upstream planning call must have stream=false even though the
	// client asked for stream=true; we need a complete JSON to extract
	// the plan from.
	var sent map[string]any
	_ = json.Unmarshal(captured, &sent)
	if v, _ := sent["stream"].(bool); v {
		t.Error("upstream planning call should have stream=false, got true")
	}

	// Response back to the client must be SSE.
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("response Content-Type = %q, want text/event-stream", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"executor ran successfully",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("SSE body missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestFullMode_MidConversationFallsThrough(t *testing.T) {
	// 2+ messages in the array means this is a continuing turn, not a fresh session.
	o := orchForTest(t)
	rewrittenSeen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// The original body had no tool_choice; a rewritten one would.
		if strings.Contains(string(body), "emit_plan") {
			rewrittenSeen = true
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_x","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"continuation"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)

	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":"more"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if rewrittenSeen {
		t.Error("mid-conversation request should NOT be rewritten with planner block")
	}
}

func TestFullMode_PlannerTextResponseUsedAsTerminal(t *testing.T) {
	// With tool_choice:auto, the planner may decide a plan isn't
	// worthwhile and answer directly. The proxy should treat that text
	// as the synthesized terminal — no executor, no second passthrough.
	o := orchForTest(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_text",
			"type":"message",
			"role":"assistant",
			"model":"claude-opus-4-7",
			"content":[{"type":"text","text":"the planner just answered directly"}],
			"usage":{"input_tokens":50,"output_tokens":12}
		}`))
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if calls != 1 {
		t.Errorf("expected exactly 1 upstream call (planner only, no fallback), got %d", calls)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "the planner just answered directly") {
		t.Errorf("synthesized response missing planner text; got: %s", body)
	}
}

func TestFullMode_TinyMaxTokensFallsThrough(t *testing.T) {
	// Claude Code fires single-message probes with max_tokens=1 (token
	// counters, conversation-prep). Rewriting them prepends ~16K tokens of
	// planner block then asks the planner to emit emit_plan in 1 output
	// token, which always truncates and falls back. Net cost is purely
	// negative. The guard should send these straight through untouched.
	o := orchForTest(t)
	rewritten := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "emit_plan") {
			rewritten = true
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if rewritten {
		t.Error("max_tokens=1 probe should NOT be rewritten with planner block")
	}
}

func TestFullMode_UpstreamErrorBodyInFallbackLog(t *testing.T) {
	// When upstream returns non-2xx on the planning call, the proxy must
	// surface the response body in its fallback log line so 4xx causes
	// (cache_control validation, oversized prompt, model-not-found) are
	// diagnosable from the proxy stderr without inspecting the trace.
	o := orchForTest(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("content-type", "application/json")
		switch calls {
		case 1:
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"cache_control: ttl too long for system block at index 2"}}`))
		case 2:
			_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
		}
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	logged := logBuf.String()
	for _, want := range []string{"HTTP 400", "cache_control: ttl too long"} {
		if !strings.Contains(logged, want) {
			t.Errorf("fallback log missing %q\nlog:\n%s", want, logged)
		}
	}
}

func TestFullMode_PlanParseErrorFallsBack(t *testing.T) {
	// First call (planning) returns a malformed plan. Second call (fallback)
	// is the original user request — we verify the fallback fires by
	// checking that exactly two upstream calls happened.
	o := orchForTest(t)
	calls := 0
	var firstBody, secondBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		w.Header().Set("content-type", "application/json")
		switch calls {
		case 1:
			firstBody = body
			// Malformed plan source — checker will reject.
			malformed := map[string]any{
				"content": []map[string]any{
					{
						"type":  "tool_use",
						"name":  "emit_plan",
						"input": map[string]any{"program": "this is not a plan"},
					},
				},
				"usage": map[string]int{"input_tokens": 100, "output_tokens": 10},
			}
			b, _ := json.Marshal(malformed)
			_, _ = w.Write(b)
		case 2:
			secondBody = body
			// Fallback: pretend Opus replied normally.
			_, _ = w.Write([]byte(`{"id":"msg_y","type":"message","role":"assistant","model":"claude-opus-4-7","content":[{"type":"text","text":"fallback reply"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
		}
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil)
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if calls != 2 {
		t.Errorf("expected 2 upstream calls (planning + fallback), got %d", calls)
	}
	// First call should have been rewritten (had emit_plan).
	if !strings.Contains(string(firstBody), "emit_plan") {
		t.Error("first call should have been the planning rewrite")
	}
	// Second call should NOT have emit_plan — it's the original passed through.
	if strings.Contains(string(secondBody), "emit_plan") {
		t.Error("fallback should forward the ORIGINAL request, not a rewritten one")
	}
	// Response content should be the fallback reply.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fallback reply") {
		t.Errorf("expected fallback reply, got: %s", body)
	}
}

func TestFullMode_ExecutorErrorFallsBack(t *testing.T) {
	// The plan parses cleanly but the executor errors (LocalBackend missing
	// for a `local` step). Should fall back to passthrough.
	o := orchForTest(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("content-type", "application/json")
		switch calls {
		case 1:
			// Plan that needs LocalBackend (which we don't wire).
			plan := fmt.Sprintf(`plan @v1 { meta { entry: s manifest: %s } s := local llama3-8b "x" -> $r => t  t := terminal "${r}" }`, o.Manifest.Hash)
			resp := map[string]any{
				"content": []map[string]any{
					{"type": "tool_use", "name": "emit_plan", "input": map[string]any{"program": plan}},
				},
				"usage": map[string]int{"input_tokens": 100, "output_tokens": 10},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)
		case 2:
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"fallback"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
		}
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), nil) // nil LocalBackend
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"x"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if calls != 2 {
		t.Errorf("expected 2 calls (planning + fallback), got %d", calls)
	}
}

// ---- LocalBackend integration through the executor --------------------

type stubLocal struct{ output string }

func (s stubLocal) RunLocal(_ context.Context, _ exec.LocalRequest) (exec.LocalResult, error) {
	return exec.LocalResult{Output: s.output}, nil
}

type errLocal struct{}

func (errLocal) RunLocal(_ context.Context, _ exec.LocalRequest) (exec.LocalResult, error) {
	return exec.LocalResult{}, errors.New("local boom")
}

func TestFullMode_LocalStepRoutedThroughBackend(t *testing.T) {
	o := orchWithModel(t, "llama3-8b")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := fmt.Sprintf(`plan @v1 { meta { entry: s manifest: %s } s := local llama3-8b "summarize" -> $out => t  t := terminal "${out}" }`, o.Manifest.Hash)
		resp := map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "name": "emit_plan", "input": map[string]any{"program": plan}},
			},
			"usage": map[string]int{"input_tokens": 100, "output_tokens": 10},
		}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	tt := NewFullModeTransport(o, upstreamTo(srv), stubLocal{output: "the summary"})
	req := newMessagesPOST(t, `{"model":"claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"x"}]}`)
	resp, err := tt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "the summary") {
		t.Errorf("expected backend output in synthesized response, got: %s", body)
	}
}

// ---- Helpers ----------------------------------------------------------

func newMessagesPOST(t *testing.T, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("content-type", "application/json")
	return r
}
