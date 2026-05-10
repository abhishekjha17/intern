package orchestrator

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// rewriteForPlanning takes the user's outgoing /v1/messages request body
// and returns a rewritten body that asks Opus to emit a plan@v1 program
// instead of replying directly. The transformations are:
//
//  1. Prepend the planner system block (with cache_control: ephemeral)
//     to the existing system content. A pre-existing system field is
//     preserved as a second cache-controlled block so any client-defined
//     instructions still flow through to the planner. The planner block's
//     cache_control TTL matches the longest TTL among the existing
//     blocks, so the cache hierarchy (longer TTL first) stays valid —
//     otherwise Anthropic returns 400 when clients like Claude Code
//     attach `ttl: "1h"` blocks of their own.
//  2. Replace `tools` with [emit_plan] only — client tools are advertised
//     to the planner via the prompt block, not via the API tools array.
//  3. Set `tool_choice` to force emit_plan invocation.
//  4. Force `stream: false`. Streaming is handled by the caller; we need
//     a complete JSON response to extract the plan from.
//
// All other fields (model, messages, max_tokens, temperature, etc.)
// pass through untouched.
func rewriteForPlanning(reqBody []byte, plannerBlock string, emitPlanTool json.RawMessage) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(reqBody, &raw); err != nil {
		return nil, fmt.Errorf("rewrite: parse request body: %w", err)
	}

	cc := map[string]string{"type": "ephemeral"}
	if ttl := maxSystemTTL(raw["system"]); ttl != "" {
		cc["ttl"] = ttl
	}
	plannerBlockJSON, err := json.Marshal(map[string]any{
		"type":          "text",
		"text":          plannerBlock,
		"cache_control": cc,
	})
	if err != nil {
		return nil, err
	}

	newSystem, err := mergeSystem(raw["system"], plannerBlockJSON)
	if err != nil {
		return nil, err
	}
	raw["system"] = newSystem

	tools, err := json.Marshal([]json.RawMessage{emitPlanTool})
	if err != nil {
		return nil, err
	}
	raw["tools"] = tools

	// Anthropic rejects forced tool_choice when `thinking` is enabled
	// (Claude Code sets it for Opus-4.7). `auto` is compatible with
	// thinking and, since we only advertise the emit_plan tool, the
	// model still naturally calls it when a plan helps. When the model
	// chooses to answer directly with text instead, the caller treats
	// that text as the synthesized terminal — no second passthrough,
	// thinking-quality preserved either way.
	raw["tool_choice"] = json.RawMessage(`{"type":"auto"}`)
	raw["stream"] = json.RawMessage(`false`)

	return json.Marshal(raw)
}

// maxSystemTTL inspects the cache_control entries on each block of the
// incoming `system` field and returns the longest TTL it sees, or "" if
// no cache_control is present (caller defaults to 5m). Anthropic requires
// blocks with longer TTLs to appear earlier in the prompt; since we
// prepend our planner block, it must use a TTL >= the maximum found.
//
// Recognized values: "1h" (longest) and "5m" (or unset, the default).
// Anything else returns "" — we don't try to be clever about unknown
// TTL strings.
func maxSystemTTL(existing json.RawMessage) string {
	if len(existing) == 0 || string(existing) == "null" {
		return ""
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(existing, &arr); err != nil {
		// String form has no cache_control.
		return ""
	}
	has1h := false
	for _, blk := range arr {
		var b struct {
			CacheControl struct {
				TTL string `json:"ttl"`
			} `json:"cache_control"`
		}
		if err := json.Unmarshal(blk, &b); err != nil {
			continue
		}
		if b.CacheControl.TTL == "1h" {
			has1h = true
		}
	}
	if has1h {
		return "1h"
	}
	return ""
}

// mergeSystem prepends plannerBlock to whatever was in the existing
// system field. Anthropic accepts either a string or an array of content
// blocks for `system`; we normalize to an array.
func mergeSystem(existing json.RawMessage, plannerBlock json.RawMessage) (json.RawMessage, error) {
	if len(existing) == 0 || string(existing) == "null" {
		out, err := json.Marshal([]json.RawMessage{plannerBlock})
		return out, err
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(existing, &s); err == nil {
		userBlock, err := json.Marshal(map[string]any{
			"type":          "text",
			"text":          s,
			"cache_control": map[string]string{"type": "ephemeral"},
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal([]json.RawMessage{plannerBlock, userBlock})
	}
	// Otherwise array.
	var arr []json.RawMessage
	if err := json.Unmarshal(existing, &arr); err != nil {
		return nil, fmt.Errorf("rewrite: system field is neither string nor array: %w", err)
	}
	return json.Marshal(append([]json.RawMessage{plannerBlock}, arr...))
}

// synthesizeResponse builds an Anthropic-shaped /v1/messages JSON body
// carrying terminalText as a single text content block. The response is
// what the proxy returns to the client when full mode succeeds — the
// client thinks it got a normal Claude reply.
//
// Token counts are approximate (4 chars per token rule of thumb); they
// are recorded so trace-logging cost analysis still produces meaningful
// numbers, even though no extra tokens were billed for the executor's
// local work.
func synthesizeResponse(model, terminalText string, planningInputTokens, planningOutputTokens int) ([]byte, error) {
	id, err := newMessageID()
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"id":    id,
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]any{
			{"type": "text", "text": terminalText},
		},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":          planningInputTokens,
			"output_tokens":         planningOutputTokens + estimateTokens(terminalText),
			"cache_read_input_tokens": 0,
		},
	}
	_ = time.Now // future: include in id
	return json.Marshal(body)
}

// newMessageID returns an Anthropic-shaped message ID (msg_<32 hex>).
// Used so logger/trace tooling and clients can treat the synthesized
// response as a normal Claude reply for indexing purposes.
func newMessageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "msg_" + hex.EncodeToString(b[:]), nil
}

// estimateTokens returns a rough token count using the 4-char rule.
// Used only for the synthesized usage field; precise counts would
// require a tokenizer.
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	n := len(s) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// synthesizeSSE builds an Anthropic-shaped SSE event stream carrying
// terminalText as a single text content block. The stream is the same
// sequence of events Anthropic emits for a non-tool message reply:
// message_start → content_block_start → content_block_delta+ →
// content_block_stop → message_delta → message_stop.
//
// The output is a complete buffer rather than a true streaming pipe;
// SDK clients accept either, and the executor only produces the final
// terminal text after all local steps complete, so progressive output
// would not change end-to-end latency.
//
// terminalText is split into ~256-byte deltas so SDKs that surface
// partial messages get multiple chunks rather than a single jumbo one.
func synthesizeSSE(model, terminalText string, planningInputTokens, planningOutputTokens int) ([]byte, error) {
	id, err := newMessageID()
	if err != nil {
		return nil, err
	}

	output := planningOutputTokens + estimateTokens(terminalText)

	var buf bytes.Buffer

	writeEvent := func(name string, data any) error {
		js, err := json.Marshal(data)
		if err != nil {
			return err
		}
		buf.WriteString("event: ")
		buf.WriteString(name)
		buf.WriteString("\ndata: ")
		buf.Write(js)
		buf.WriteString("\n\n")
		return nil
	}

	if err := writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  planningInputTokens,
				"output_tokens": 1,
			},
		},
	}); err != nil {
		return nil, err
	}

	if err := writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	}); err != nil {
		return nil, err
	}

	if err := writeEvent("ping", map[string]any{"type": "ping"}); err != nil {
		return nil, err
	}

	const chunkSize = 256
	for i := 0; i < len(terminalText); i += chunkSize {
		end := i + chunkSize
		if end > len(terminalText) {
			end = len(terminalText)
		}
		if err := writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": terminalText[i:end],
			},
		}); err != nil {
			return nil, err
		}
	}

	if err := writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	}); err != nil {
		return nil, err
	}

	if err := writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": output},
	}); err != nil {
		return nil, err
	}

	if err := writeEvent("message_stop", map[string]any{
		"type": "message_stop",
	}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
