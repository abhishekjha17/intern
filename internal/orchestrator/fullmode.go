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
	"strings"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/exec"
	"github.com/abhishekjha17/intern/internal/orchestrator/planner"
)

// FullModeTransport is an http.RoundTripper that intercepts /v1/messages
// requests, swaps in a planning prompt, runs the executor against the
// returned plan@v1 program, and synthesizes an Anthropic-shaped reply
// back to the client. Any failure (parse, semantic check, runtime error
// not handled by a catch/on_err clause) causes a clean fall-back to
// forwarding the original request to upstream — the client sees a normal
// Claude response either way.
//
// Wraps an inner RoundTripper (typically logger.LoggingRoundTripper) so
// trace logging continues to capture both the planning call and the
// fall-back forwarded call.
type FullModeTransport struct {
	Inner        http.RoundTripper
	Orch         *Orchestrator
	LocalBackend exec.LocalBackend

	// PlanningTimeout caps the synchronous planning + execution flow.
	// Defaults to 60s; long-running local steps inside an executor run
	// can exhaust this. Hitting the timeout falls back to passthrough.
	PlanningTimeout time.Duration
}

// minPlanningMaxTokens skips interception when the original request's
// max_tokens is below this. Claude Code fires lots of single-message
// probes with max_tokens=1 (token counters, conversation prep, routing);
// rewriting them prepends the planner block (~16K tokens) and forces the
// planner to emit emit_plan in 1 output token, which always truncates and
// fails. Net effect would be paying for cache creation on a probe that
// never benefits from a plan.
const minPlanningMaxTokens = 512

// NewFullModeTransport returns a transport configured against the
// orchestrator's loaded manifest and a LocalBackend. PlanningTimeout
// defaults to 60s.
func NewFullModeTransport(o *Orchestrator, inner http.RoundTripper, local exec.LocalBackend) *FullModeTransport {
	return &FullModeTransport{
		Inner:           inner,
		Orch:            o,
		LocalBackend:    local,
		PlanningTimeout: 60 * time.Second,
	}
}

// RoundTrip implements http.RoundTripper. The decision tree:
//
//   - Not POST /v1/messages → forward unchanged.
//   - Not first-of-session (more than one message, or not a user-only
//     opener) → forward unchanged.
//   - Otherwise: plan, execute, synthesize. Streaming requests get an
//     SSE-shaped synthesized response; non-streaming get JSON. Any
//     error → fall back to forwarding the original request unchanged.
func (t *FullModeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !shouldIntercept(req) {
		return t.Inner.RoundTrip(req)
	}

	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		return t.Inner.RoundTrip(req)
	}
	req.Body = io.NopCloser(bytes.NewReader(reqBody))

	first, streaming, maxTokens := probeRequest(reqBody)
	if !first || maxTokens < minPlanningMaxTokens {
		return t.Inner.RoundTrip(req)
	}

	resp, err := t.handlePlan(req, reqBody, streaming)
	if err != nil {
		log.Printf("intern/orchestrator: full-mode fell back to passthrough: %v", err)
		// Restore body for the inner roundtripper.
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		return t.Inner.RoundTrip(req)
	}
	return resp, nil
}

// shouldIntercept gates which requests go through the planning path.
// We narrowly target the Messages API; everything else (token counting,
// model listing, files, batch) flows straight through to upstream.
func shouldIntercept(req *http.Request) bool {
	if req.Method != http.MethodPost {
		return false
	}
	return strings.HasSuffix(req.URL.Path, "/v1/messages")
}

// probeRequest returns (firstOfSession, streaming, maxTokens). The opener
// is a single message with role=user; streaming is whatever the client
// asked for; maxTokens is the original max_tokens value (0 if unset).
// Continuing turns and tiny-budget probes fall through to upstream
// untouched; the streaming flag controls whether the synthesized response
// is SSE or plain JSON.
func probeRequest(body []byte) (bool, bool, int) {
	var probe struct {
		Stream    bool `json:"stream"`
		MaxTokens int  `json:"max_tokens"`
		Messages  []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false, false, 0
	}
	if len(probe.Messages) != 1 {
		return false, probe.Stream, probe.MaxTokens
	}
	return probe.Messages[0].Role == "user", probe.Stream, probe.MaxTokens
}

// handlePlan runs the rewrite → upstream → extract → execute →
// synthesize chain. Returns a fully-formed *http.Response on success
// or an error for the caller to fall back from. When streaming is
// true, the synthesized response uses Anthropic's SSE event stream;
// otherwise it's a single JSON document.
func (t *FullModeTransport) handlePlan(req *http.Request, reqBody []byte, streaming bool) (*http.Response, error) {
	rewritten, err := rewriteForPlanning(reqBody, t.Orch.PlannerBlock(), t.Orch.EmitPlanTool())
	if err != nil {
		return nil, fmt.Errorf("rewrite: %w", err)
	}

	planReq := req.Clone(req.Context())
	planReq.Body = io.NopCloser(bytes.NewReader(rewritten))
	planReq.ContentLength = int64(len(rewritten))
	planReq.Header.Set("content-type", "application/json")

	planResp, err := t.Inner.RoundTrip(planReq)
	if err != nil {
		return nil, fmt.Errorf("upstream planning call: %w", err)
	}
	defer planResp.Body.Close()

	respBody, err := io.ReadAll(planResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read planning response: %w", err)
	}

	if planResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream planning HTTP %d: %s",
			planResp.StatusCode, truncate(string(respBody), 240))
	}

	planTokens, _ := extractPlanningUsage(respBody)
	model, _ := extractModel(reqBody)

	emitPlan, err := planner.ExtractFromMessageJSON(respBody)
	var terminal string
	switch {
	case err == nil:
		// Planner emitted a plan — execute it locally.
		cctx, cancel := context.WithTimeout(req.Context(), t.PlanningTimeout)
		defer cancel()
		terminal, _, err = t.runWithBackend(cctx, emitPlan.Program)
		if err != nil {
			return nil, fmt.Errorf("execute plan: %w", err)
		}
		if terminal == "" {
			return nil, errors.New("plan terminated without a terminal message")
		}
	case errors.Is(err, planner.ErrNoEmitPlan):
		// Planner skipped emit_plan and answered in text. With
		// tool_choice:auto this is a valid signal: the model judged a
		// plan unhelpful and replied directly. Use that text as the
		// synthesized terminal — no executor invocation, no second
		// passthrough, thinking-quality preserved.
		terminal = extractAssistantText(respBody)
		if terminal == "" {
			return nil, errors.New("planner returned neither emit_plan nor non-empty text")
		}
	default:
		return nil, fmt.Errorf("extract emit_plan: %w", err)
	}

	if streaming {
		synthBody, err := synthesizeSSE(model, terminal, planTokens.input, planTokens.output)
		if err != nil {
			return nil, fmt.Errorf("synthesize SSE response: %w", err)
		}
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    200,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:          io.NopCloser(bytes.NewReader(synthBody)),
			ContentLength: int64(len(synthBody)),
			Request:       req,
		}, nil
	}

	synthBody, err := synthesizeResponse(model, terminal, planTokens.input, planTokens.output)
	if err != nil {
		return nil, fmt.Errorf("synthesize response: %w", err)
	}

	return &http.Response{
		Status:        "200 OK",
		StatusCode:    200,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(synthBody)),
		ContentLength: int64(len(synthBody)),
		Request:       req,
	}, nil
}

// runWithBackend executes src against the orchestrator's check context
// and the configured LocalBackend. Mirrors Orchestrator.ExecutePlan but
// with the backend wired in so `local` plan steps actually invoke
// vllm-mlx (or whichever LocalBackend is plugged in).
func (t *FullModeTransport) runWithBackend(ctx context.Context, src string) (string, *exec.Result, error) {
	p, errs, err := t.Orch.CheckPlan(src)
	if err != nil {
		return "", nil, err
	}
	if len(errs) > 0 {
		return "", nil, errs[0]
	}
	ex := &exec.Executor{
		Tools: t.Orch.Tools,
		Local: t.LocalBackend,
	}
	res, err := ex.Run(ctx, p)
	if err != nil {
		return "", res, err
	}
	return res.Terminal, res, nil
}

// planningUsage carries the input/output token counts off Anthropic's
// usage object so the synthesized response can echo realistic numbers
// to trace logging.
type planningUsage struct{ input, output int }

func extractPlanningUsage(body []byte) (planningUsage, error) {
	var u struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return planningUsage{}, err
	}
	return planningUsage{input: u.Usage.InputTokens, output: u.Usage.OutputTokens}, nil
}

// extractAssistantText concatenates the text from every `text` block in
// an Anthropic /v1/messages response body. Used when tool_choice:auto
// produced a direct text reply instead of an emit_plan tool_use, so the
// caller can use that text as the synthesized terminal. Returns "" if
// the body is malformed or has no text content.
func extractAssistantText(body []byte) string {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	var out strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	return out.String()
}

// truncate clips s to at most n bytes, suffixing "…" when cut. Used to
// keep upstream error messages out of trace bloat / log spam while still
// surfacing enough of the body to diagnose 4xx/5xx responses.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func extractModel(body []byte) (string, error) {
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", err
	}
	return m.Model, nil
}
