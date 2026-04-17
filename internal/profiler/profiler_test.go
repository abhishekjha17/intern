package profiler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abhishekjha17/intern/internal/logger"
)

// --------------- extract.go tests ---------------

func TestExtractResponse_SSE_Blocks(t *testing.T) {
	sse := lines(
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","name":""}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","name":""}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"Read"}}`,
	)
	rf := ExtractResponse(sse)
	want := []BlockInfo{
		{Type: "thinking"},
		{Type: "text"},
		{Type: "tool_use", Name: "Read"},
	}
	if len(rf.Blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(rf.Blocks), len(want))
	}
	for i, b := range rf.Blocks {
		if b.Type != want[i].Type || b.Name != want[i].Name {
			t.Errorf("block[%d] = %+v, want %+v", i, b, want[i])
		}
	}
}

func TestExtractResponse_JSON_Blocks(t *testing.T) {
	resp := `{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Edit","id":"x","input":{}}]}`
	rf := ExtractResponse(resp)
	if len(rf.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(rf.Blocks))
	}
	if rf.Blocks[0].Type != "text" {
		t.Errorf("block[0].Type = %q, want text", rf.Blocks[0].Type)
	}
	if rf.Blocks[1].Type != "tool_use" || rf.Blocks[1].Name != "Edit" {
		t.Errorf("block[1] = %+v, want tool_use/Edit", rf.Blocks[1])
	}
}

func TestExtractToolsCalled(t *testing.T) {
	blocks := []BlockInfo{
		{Type: "thinking"},
		{Type: "text"},
		{Type: "tool_use", Name: "Read"},
		{Type: "server_tool_use", Name: "WebSearch"},
		{Type: "text"},
	}
	tools := extractToolsCalled(blocks)
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "WebSearch" {
		t.Errorf("got %v, want [Read WebSearch]", tools)
	}
}

func TestClassifyOutputType(t *testing.T) {
	tests := []struct {
		blocks []BlockInfo
		want   string
	}{
		{[]BlockInfo{{Type: "text"}}, "text_only"},
		{[]BlockInfo{{Type: "tool_use", Name: "Read"}}, "tool_calls_only"},
		{[]BlockInfo{{Type: "text"}, {Type: "tool_use", Name: "Edit"}}, "mixed"},
		{nil, "empty"},
	}
	for _, tt := range tests {
		got := classifyOutputType(tt.blocks)
		if got != tt.want {
			t.Errorf("classifyOutputType(%v) = %q, want %q", tt.blocks, got, tt.want)
		}
	}
}

func TestExtractResponse_SSE_ThinkingWithText(t *testing.T) {
	sse := lines(
		"event: content_block_start",
		`data: {"type":"content_block_start","content_block":{"type":"thinking"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hmm"}}`,
	)
	rf := ExtractResponse(sse)
	if !rf.HasThinkingBlock || !rf.HasThinkingText {
		t.Errorf("got HasThinkingBlock=%v HasThinkingText=%v, want true/true", rf.HasThinkingBlock, rf.HasThinkingText)
	}
}

func TestExtractResponse_SSE_SignatureOnly(t *testing.T) {
	sse := lines(
		"event: content_block_start",
		`data: {"type":"content_block_start","content_block":{"type":"thinking"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","delta":{"type":"signature_delta","signature":"abc"}}`,
	)
	rf := ExtractResponse(sse)
	if !rf.HasThinkingBlock || rf.HasThinkingText {
		t.Errorf("got HasThinkingBlock=%v HasThinkingText=%v, want true/false", rf.HasThinkingBlock, rf.HasThinkingText)
	}
}

func TestExtractResponse_JSON_Thinking(t *testing.T) {
	resp := `{"content":[{"type":"thinking","thinking":"let me think"},{"type":"text","text":"hello"}]}`
	rf := ExtractResponse(resp)
	if !rf.HasThinkingBlock || !rf.HasThinkingText {
		t.Errorf("got HasThinkingBlock=%v HasThinkingText=%v, want true/true", rf.HasThinkingBlock, rf.HasThinkingText)
	}
}

func TestExtractResponse_NoThinking(t *testing.T) {
	resp := `{"content":[{"type":"text","text":"hello"}]}`
	rf := ExtractResponse(resp)
	if rf.HasThinkingBlock || rf.HasThinkingText {
		t.Errorf("got HasThinkingBlock=%v HasThinkingText=%v, want false/false", rf.HasThinkingBlock, rf.HasThinkingText)
	}
}

func TestExtractResponse_SSE_CacheCreation(t *testing.T) {
	sse := lines(
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":5000}}}`,
	)
	rf := ExtractResponse(sse)
	if rf.CacheCreation != 5000 {
		t.Errorf("got %d, want 5000", rf.CacheCreation)
	}
}

func TestExtractResponse_JSON_CacheCreation(t *testing.T) {
	resp := `{"usage":{"input_tokens":100,"cache_creation_input_tokens":3000}}`
	rf := ExtractResponse(resp)
	if rf.CacheCreation != 3000 {
		t.Errorf("got %d, want 3000", rf.CacheCreation)
	}
}

func TestExtractMaxTokens(t *testing.T) {
	req := `{"model":"claude-opus-4-6","max_tokens":16000,"messages":[]}`
	got := extractMaxTokens(req)
	if got != 16000 {
		t.Errorf("got %d, want 16000", got)
	}
}

func TestExtractMaxTokens_One(t *testing.T) {
	req := `{"model":"claude-opus-4-6","max_tokens":1,"messages":[]}`
	got := extractMaxTokens(req)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestExtractHasToolResult_True(t *testing.T) {
	req := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"done"}]}]}`
	if !extractHasToolResult(req) {
		t.Error("expected true for request with tool_result")
	}
}

func TestExtractHasToolResult_False(t *testing.T) {
	req := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	if extractHasToolResult(req) {
		t.Error("expected false for request without tool_result")
	}
}

func TestExtractMessageCount(t *testing.T) {
	req := `{"messages":[{},{},{}]}`
	got := extractMessageCount(req)
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestExtractResponse_JSON_BashCommands(t *testing.T) {
	resp := `{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}},{"type":"tool_use","name":"Read","input":{"file":"x"}},{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]}`
	rf := ExtractResponse(resp)
	if len(rf.BashCommands) != 2 || rf.BashCommands[0] != "go test ./..." || rf.BashCommands[1] != "git status" {
		t.Errorf("got %v, want [go test ./... git status]", rf.BashCommands)
	}
}

func TestExtractResponse_SSE_BashCommands(t *testing.T) {
	sse := lines(
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Bash"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\"}"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
	)
	rf := ExtractResponse(sse)
	if len(rf.BashCommands) != 1 || rf.BashCommands[0] != "ls -la" {
		t.Errorf("got %v, want [ls -la]", rf.BashCommands)
	}
}

func TestExtractResponse_SSE_AllFields(t *testing.T) {
	// Test that a single pass extracts blocks, thinking, cache creation, and bash commands together.
	sse := lines(
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":2000}}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"Bash"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test\"}"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":2}`,
	)
	rf := ExtractResponse(sse)

	if len(rf.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(rf.Blocks))
	}
	if !rf.HasThinkingBlock || !rf.HasThinkingText {
		t.Errorf("thinking: got block=%v text=%v, want true/true", rf.HasThinkingBlock, rf.HasThinkingText)
	}
	if rf.CacheCreation != 2000 {
		t.Errorf("cache creation: got %d, want 2000", rf.CacheCreation)
	}
	if len(rf.BashCommands) != 1 || rf.BashCommands[0] != "go test" {
		t.Errorf("bash commands: got %v, want [go test]", rf.BashCommands)
	}
}

// --------------- classify.go tests ---------------

func TestClassifyPhase_Exploration(t *testing.T) {
	got := ClassifyPhase([]string{"Read", "Grep", "Glob"}, nil)
	if got != "exploration" {
		t.Errorf("got %q, want exploration", got)
	}
}

func TestClassifyPhase_Execution(t *testing.T) {
	got := ClassifyPhase([]string{"Edit", "Write"}, nil)
	if got != "execution" {
		t.Errorf("got %q, want execution", got)
	}
}

func TestClassifyPhase_Verification(t *testing.T) {
	got := ClassifyPhase([]string{"Bash"}, []string{"go test ./..."})
	if got != "verification" {
		t.Errorf("got %q, want verification", got)
	}
}

func TestClassifyPhase_Planning(t *testing.T) {
	got := ClassifyPhase([]string{"EnterPlanMode", "AskUserQuestion"}, nil)
	if got != "planning" {
		t.Errorf("got %q, want planning", got)
	}
}

func TestClassifyPhase_Conversation(t *testing.T) {
	got := ClassifyPhase(nil, nil)
	if got != "conversation" {
		t.Errorf("got %q, want conversation", got)
	}
}

func TestClassifyPhase_BashExploration(t *testing.T) {
	got := ClassifyPhase([]string{"Bash"}, []string{"ls -la /tmp"})
	if got != "exploration" {
		t.Errorf("got %q, want exploration", got)
	}
}

func TestClassifyPhase_BashGitPush(t *testing.T) {
	got := ClassifyPhase([]string{"Bash"}, []string{"git push origin main"})
	if got != "execution" {
		t.Errorf("got %q, want execution", got)
	}
}

func TestClassifyComplexity_Trivial(t *testing.T) {
	msg := &MessageProfile{MaxTokens: 1}
	got := ClassifyComplexity(msg)
	if got != "trivial" {
		t.Errorf("got %q, want trivial", got)
	}
}

func TestClassifyComplexity_Mechanical(t *testing.T) {
	msg := &MessageProfile{
		MaxTokens:    16000,
		OutputTokens: 100,
		ToolsCalled:  []string{"Read"},
	}
	got := ClassifyComplexity(msg)
	if got != "mechanical" {
		t.Errorf("got %q, want mechanical", got)
	}
}

func TestClassifyComplexity_Reasoning(t *testing.T) {
	msg := &MessageProfile{
		MaxTokens:    16000,
		OutputTokens: 500,
		ToolsCalled:  []string{"Read", "Grep", "Edit"},
		HasThinking:  true,
		OutputType:   "mixed",
	}
	// score: output 500 → +2, tools 3 → +2, diversity 3 → +2, thinking → +2, mixed → +1 = 9 → creative
	got := ClassifyComplexity(msg)
	if got != "creative" {
		t.Errorf("got %q, want creative", got)
	}
}

func TestClassifyComplexity_LowScore(t *testing.T) {
	msg := &MessageProfile{
		MaxTokens:    16000,
		OutputTokens: 20,
	}
	got := ClassifyComplexity(msg)
	if got != "trivial" {
		t.Errorf("got %q, want trivial", got)
	}
}

func TestClassifyDependency_Independent(t *testing.T) {
	req := `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`
	got := ClassifyDependency(req)
	if got != "independent" {
		t.Errorf("got %q, want independent", got)
	}
}

func TestClassifyDependency_ToolContinuation(t *testing.T) {
	req := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"done"}]}]}`
	got := ClassifyDependency(req)
	if got != "tool_continuation" {
		t.Errorf("got %q, want tool_continuation", got)
	}
}

func TestClassifyDependency_Conversation(t *testing.T) {
	req := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"how are you"}]}`
	got := ClassifyDependency(req)
	if got != "conversation_continuation" {
		t.Errorf("got %q, want conversation_continuation", got)
	}
}

func TestClassifyOffload_HealthCheck(t *testing.T) {
	msg := &MessageProfile{MaxTokens: 1}
	ok, reason := ClassifyOffload(msg)
	if !ok || reason != "health_check" {
		t.Errorf("got (%v, %q), want (true, health_check)", ok, reason)
	}
}

func TestClassifyOffload_ToolContinuation(t *testing.T) {
	msg := &MessageProfile{
		Dependency:   "tool_continuation",
		OutputTokens: 100,
	}
	ok, reason := ClassifyOffload(msg)
	if !ok || reason != "tool_result_continuation" {
		t.Errorf("got (%v, %q), want (true, tool_result_continuation)", ok, reason)
	}
}

func TestClassifyOffload_Trivial(t *testing.T) {
	msg := &MessageProfile{Complexity: "trivial"}
	ok, reason := ClassifyOffload(msg)
	if !ok || reason != "trivial_task" {
		t.Errorf("got (%v, %q), want (true, trivial_task)", ok, reason)
	}
}

func TestClassifyOffload_NotOffloadable(t *testing.T) {
	msg := &MessageProfile{
		MaxTokens:    16000,
		Complexity:   "reasoning",
		Dependency:   "independent",
		OutputTokens: 2000,
	}
	ok, reason := ClassifyOffload(msg)
	if ok {
		t.Errorf("got (%v, %q), want (false, \"\")", ok, reason)
	}
}

// --------------- pricing.go tests ---------------

func TestCostForTokens(t *testing.T) {
	// 1000 input tokens of opus at $5/MTok = $0.005
	cost := CostForTokens("claude-opus-4-6", 1000, 0, 0, 0)
	if fmt.Sprintf("%.6f", cost) != "0.005000" {
		t.Errorf("got %f, want 0.005", cost)
	}

	// 1000 output tokens of opus at $25/MTok = $0.025
	cost = CostForTokens("claude-opus-4-6", 0, 1000, 0, 0)
	if fmt.Sprintf("%.6f", cost) != "0.025000" {
		t.Errorf("got %f, want 0.025", cost)
	}

	// Unknown model returns 0
	cost = CostForTokens("unknown-model", 1000, 1000, 0, 0)
	if cost != 0 {
		t.Errorf("got %f, want 0 for unknown model", cost)
	}
}

// --------------- integration test ---------------

func TestAnalyze(t *testing.T) {
	now := time.Now()
	traces := []logger.Trace{
		{
			Timestamp:       now,
			SessionID:       "sess1",
			Model:           "claude-opus-4-6",
			InputTokens:     1000,
			OutputTokens:    200,
			CacheReadTokens: 500,
			Request:         `{"max_tokens":16000,"messages":[{"role":"user","content":"hello"}]}`,
			Response:        `{"content":[{"type":"text","text":"hi there"}]}`,
		},
		{
			Timestamp:       now.Add(time.Minute),
			SessionID:       "sess1",
			Model:           "claude-opus-4-6",
			InputTokens:     2000,
			OutputTokens:    500,
			CacheReadTokens: 0,
			Request:         `{"max_tokens":16000,"messages":[{"role":"user","content":"edit file"},{"role":"assistant","content":"ok"},{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"done"}]}]}`,
			Response:        `{"content":[{"type":"text","text":"done"},{"type":"tool_use","name":"Edit","id":"t1","input":{}}]}`,
		},
		{
			Timestamp:       now.Add(2 * time.Minute),
			SessionID:       "sess1",
			Model:           "claude-opus-4-6",
			InputTokens:     500,
			OutputTokens:    10,
			CacheReadTokens: 0,
			Request:         `{"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`,
			Response:        `{"content":[{"type":"text","text":""}]}`,
		},
		{
			Timestamp:       now.Add(3 * time.Minute),
			SessionID:       "sess2",
			Model:           "claude-haiku-4-5-20251001",
			InputTokens:     800,
			OutputTokens:    100,
			CacheReadTokens: 200,
			Request:         `{"max_tokens":4096,"messages":[{"role":"user","content":"search for files"}]}`,
			Response:        `{"content":[{"type":"tool_use","name":"Grep","id":"t2","input":{"pattern":"test"}}]}`,
		},
	}

	report := Analyze(traces, []string{"test.jsonl"})

	// Basic checks
	if len(report.Messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(report.Messages))
	}

	// Cost should be non-zero
	if report.Cost.GrandTotal <= 0 {
		t.Error("grand total cost should be > 0")
	}
	if len(report.Cost.ByModel) != 2 {
		t.Errorf("got %d models in cost, want 2", len(report.Cost.ByModel))
	}

	// Phase checks
	if report.Messages[0].Phase != "conversation" {
		t.Errorf("msg[0] phase = %q, want conversation", report.Messages[0].Phase)
	}
	if report.Messages[1].Phase != "execution" {
		t.Errorf("msg[1] phase = %q, want execution", report.Messages[1].Phase)
	}
	if report.Messages[2].Phase != "conversation" {
		t.Errorf("msg[2] phase = %q, want conversation", report.Messages[2].Phase)
	}
	if report.Messages[3].Phase != "exploration" {
		t.Errorf("msg[3] phase = %q, want exploration", report.Messages[3].Phase)
	}

	// Complexity: msg[2] has max_tokens=1 → trivial
	if report.Messages[2].Complexity != "trivial" {
		t.Errorf("msg[2] complexity = %q, want trivial", report.Messages[2].Complexity)
	}

	// Offload: msg[2] is health check
	if !report.Messages[2].IsOffloadCandidate || report.Messages[2].OffloadReason != "health_check" {
		t.Errorf("msg[2] offload = (%v, %q), want (true, health_check)",
			report.Messages[2].IsOffloadCandidate, report.Messages[2].OffloadReason)
	}

	// Dependency: msg[1] has tool_result in request
	if report.Messages[1].Dependency != "tool_continuation" {
		t.Errorf("msg[1] dependency = %q, want tool_continuation", report.Messages[1].Dependency)
	}

	// Sessions
	if len(report.Sessions) != 2 {
		t.Errorf("got %d sessions, want 2", len(report.Sessions))
	}

	// Tools
	if report.Tools.TotalCalls == 0 {
		t.Error("expected some tool calls")
	}

	// Render should not panic
	var buf strings.Builder
	RenderText(&buf, report)
	if buf.Len() == 0 {
		t.Error("text render produced empty output")
	}

	buf.Reset()
	if err := RenderJSON(&buf, report); err != nil {
		t.Fatalf("JSON render failed: %v", err)
	}
	var check map[string]interface{}
	if err := json.Unmarshal([]byte(buf.String()), &check); err != nil {
		t.Fatalf("JSON output is invalid: %v", err)
	}
}

// helper to join lines with newlines
func lines(ss ...string) string {
	return strings.Join(ss, "\n") + "\n"
}
