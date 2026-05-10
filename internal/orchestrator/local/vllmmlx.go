// Package local implements concrete backends for the orchestrator's
// `local` plan step. The vllm-mlx backend speaks the OpenAI-compatible
// /v1/chat/completions API and is the production path for on-device
// Apple Silicon hosting.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/abhishekjha17/intern/internal/orchestrator/exec"
	"github.com/abhishekjha17/intern/internal/orchestrator/manifest"
)

// VLLMMLX is a LocalBackend that dispatches to one or more vllm-mlx
// (or any OpenAI-compatible) servers selected by the plan step's
// model_id. Per-model overrides come from manifest entries —
// endpoint, server-side model name, and ExtraRequestParams (e.g.
// chat_template_kwargs to suppress Qwen3's thinking blocks).
type VLLMMLX struct {
	Manifest *manifest.Manifest
	Client   *http.Client
}

// NewVLLMMLX constructs a backend with a 60s default per-request HTTP
// client. Override Client to plug in a different timeout, transport,
// or to mock for tests.
func NewVLLMMLX(m *manifest.Manifest) *VLLMMLX {
	return &VLLMMLX{
		Manifest: m,
		Client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// RunLocal implements exec.LocalBackend. The flow:
//  1. Resolve model_id → manifest entry (endpoint, server model name).
//  2. Build a single-shot chat completion request against /v1/chat/completions.
//  3. Merge the manifest's ExtraRequestParams (chat_template_kwargs etc.).
//  4. Decode the response, optionally parse as JSON if format=json.
//  5. Defensively strip leaked <think>...</think> blocks.
//
// Tools-in-loop (`tools=[...]` on the local step) is not supported in
// v0; calls with non-empty Tools error explicitly so plans escalate
// rather than silently produce worse output.
func (b *VLLMMLX) RunLocal(ctx context.Context, req exec.LocalRequest) (exec.LocalResult, error) {
	if b.Manifest == nil {
		return exec.LocalResult{}, fmt.Errorf("local backend: nil manifest")
	}
	mod := b.Manifest.FindModel(req.ModelID)
	if mod == nil {
		return exec.LocalResult{}, fmt.Errorf("local backend: unknown model_id %q", req.ModelID)
	}
	if len(req.Tools) > 0 {
		return exec.LocalResult{}, fmt.Errorf(
			"local backend: tools=[...] not supported in v0; use internal_tool steps instead (model_id=%s)",
			req.ModelID)
	}

	maxTokens := mod.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	body := chatRequest{
		Model: mod.Model,
		Messages: []chatMessage{
			{Role: "user", Content: req.Prompt},
		},
		MaxTokens: maxTokens,
		Stream:    false,
	}

	if extra, ok := mod.ExtraRequestParams["chat_template_kwargs"]; ok {
		body.ChatTemplateKwargs = coerceMap(extra)
	}
	if req.Format == "json" {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return exec.LocalResult{}, fmt.Errorf("local backend: marshal: %w", err)
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := strings.TrimRight(mod.Endpoint, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(cctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return exec.LocalResult{}, err
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := b.Client.Do(httpReq)
	if err != nil {
		return exec.LocalResult{}, fmt.Errorf("local backend %s: %w", req.ModelID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return exec.LocalResult{}, fmt.Errorf("local backend %s: HTTP %d", req.ModelID, resp.StatusCode)
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return exec.LocalResult{}, fmt.Errorf("local backend %s: decode: %w", req.ModelID, err)
	}
	if len(cr.Choices) == 0 {
		return exec.LocalResult{}, fmt.Errorf("local backend %s: empty choices", req.ModelID)
	}

	text := stripThinkingBlocks(cr.Choices[0].Message.Content)

	if req.Format == "json" {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err == nil {
			return exec.LocalResult{Output: v}, nil
		}
		// Couldn't parse — return raw string. The local model probably
		// disregarded the json_object hint; treat as best-effort and let
		// downstream predicates handle it.
	}
	return exec.LocalResult{Output: text}, nil
}

// ---- HTTP wire types ----------------------------------------------------

type chatRequest struct {
	Model              string            `json:"model"`
	Messages           []chatMessage     `json:"messages"`
	MaxTokens          int               `json:"max_tokens,omitempty"`
	Stream             bool              `json:"stream"`
	ChatTemplateKwargs map[string]any    `json:"chat_template_kwargs,omitempty"`
	ResponseFormat     map[string]string `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// ---- helpers -----------------------------------------------------------

// coerceMap normalizes the YAML-decoded value of a manifest's
// extra_request_params nested map into a JSON-encodable
// map[string]any. yaml.v3 may produce map[string]any directly, or
// (for non-string keys) map[any]any — both are handled.
func coerceMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[fmt.Sprint(k)] = v
		}
		return out
	}
	return nil
}

// thinkBlock matches Qwen3-style <think>...</think> reasoning blocks.
// Some configurations leak these through to assistant content even when
// chat_template_kwargs.enable_thinking is false; the regex strips them
// from the start of the output so downstream predicates see clean text.
var thinkBlock = regexp.MustCompile(`(?s)^\s*<think>.*?</think>\s*`)

func stripThinkingBlocks(s string) string {
	return thinkBlock.ReplaceAllString(s, "")
}
