package profiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// Sum-only responses attribute all cache creation to the 5-minute bucket.
	if rf.CacheCreation5m != 3000 || rf.CacheCreation1h != 0 {
		t.Errorf("got 5m=%d 1h=%d, want 3000/0", rf.CacheCreation5m, rf.CacheCreation1h)
	}
}

func TestExtractResponse_SSE_CacheCreationBreakdown(t *testing.T) {
	sse := lines(
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":7000,"cache_creation":{"ephemeral_5m_input_tokens":5000,"ephemeral_1h_input_tokens":2000}}}}`,
	)
	rf := ExtractResponse(sse)
	if rf.CacheCreation5m != 5000 {
		t.Errorf("5m got %d, want 5000", rf.CacheCreation5m)
	}
	if rf.CacheCreation1h != 2000 {
		t.Errorf("1h got %d, want 2000", rf.CacheCreation1h)
	}
	if rf.CacheCreation != 7000 {
		t.Errorf("sum got %d, want 7000", rf.CacheCreation)
	}
}

func TestExtractResponse_JSON_CacheCreationBreakdown(t *testing.T) {
	resp := `{"usage":{"input_tokens":50,"cache_creation_input_tokens":7000,"cache_creation":{"ephemeral_5m_input_tokens":4000,"ephemeral_1h_input_tokens":3000}}}`
	rf := ExtractResponse(resp)
	if rf.CacheCreation5m != 4000 || rf.CacheCreation1h != 3000 || rf.CacheCreation != 7000 {
		t.Errorf("got 5m=%d 1h=%d sum=%d, want 4000/3000/7000",
			rf.CacheCreation5m, rf.CacheCreation1h, rf.CacheCreation)
	}
}

func TestExtractResponse_SSE_ServerToolUse(t *testing.T) {
	sse := lines(
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"server_tool_use":{"web_search_requests":3,"code_execution_requests":1}}}}`,
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":200,"server_tool_use":{"web_search_requests":5,"code_execution_requests":2}}}`,
	)
	rf := ExtractResponse(sse)
	// message_delta count wins since it arrives last.
	if rf.WebSearchRequests != 5 {
		t.Errorf("WebSearchRequests = %d, want 5", rf.WebSearchRequests)
	}
	if rf.CodeExecutionRequests != 2 {
		t.Errorf("CodeExecutionRequests = %d, want 2", rf.CodeExecutionRequests)
	}
}

func TestExtractResponse_JSON_ServerToolUse(t *testing.T) {
	resp := `{"usage":{"input_tokens":100,"output_tokens":50,"server_tool_use":{"web_search_requests":2,"code_execution_requests":0}}}`
	rf := ExtractResponse(resp)
	if rf.WebSearchRequests != 2 {
		t.Errorf("WebSearchRequests = %d, want 2", rf.WebSearchRequests)
	}
	if rf.CodeExecutionRequests != 0 {
		t.Errorf("CodeExecutionRequests = %d, want 0", rf.CodeExecutionRequests)
	}
}

func TestExtractInferenceGeo(t *testing.T) {
	if got := extractInferenceGeo(`{"model":"claude-opus-4-7","inference_geo":"us-only","messages":[]}`); got != "us-only" {
		t.Errorf("got %q, want us-only", got)
	}
	if got := extractInferenceGeo(`{"model":"claude-opus-4-7","messages":[]}`); got != "" {
		t.Errorf("missing field should return empty, got %q", got)
	}
	if got := extractInferenceGeo(`not json`); got != "" {
		t.Errorf("malformed JSON should return empty, got %q", got)
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
	cost := CostForTokens("claude-opus-4-6", 1000, 0, 0, 0, 0)
	if fmt.Sprintf("%.6f", cost) != "0.005000" {
		t.Errorf("got %f, want 0.005", cost)
	}

	// 1000 output tokens of opus at $25/MTok = $0.025
	cost = CostForTokens("claude-opus-4-6", 0, 1000, 0, 0, 0)
	if fmt.Sprintf("%.6f", cost) != "0.025000" {
		t.Errorf("got %f, want 0.025", cost)
	}

	// 5-minute vs 1-hour cache writes are priced differently.
	// 1000 tokens × $6.25/MTok (5m) = $0.00625
	cost = CostForTokens("claude-opus-4-6", 0, 0, 0, 1000, 0)
	if fmt.Sprintf("%.6f", cost) != "0.006250" {
		t.Errorf("got %f, want 0.00625 for 5m cache write", cost)
	}
	// 1000 tokens × $10/MTok (1h) = $0.010
	cost = CostForTokens("claude-opus-4-6", 0, 0, 0, 0, 1000)
	if fmt.Sprintf("%.6f", cost) != "0.010000" {
		t.Errorf("got %f, want 0.010 for 1h cache write", cost)
	}

	// Opus 4.7 must not silently price at $0 — regression guard for the
	// issue that motivated the pricing config refactor.
	cost = CostForTokens("claude-opus-4-7", 1000, 0, 0, 0, 0)
	if cost == 0 {
		t.Error("Opus 4.7 priced at $0 — pricing table is missing the new model")
	}

	// Unknown model returns 0
	cost = CostForTokens("unknown-model", 1000, 1000, 0, 0, 0)
	if cost != 0 {
		t.Errorf("got %f, want 0 for unknown model", cost)
	}
}

func TestLookupPricing_DateSuffix(t *testing.T) {
	// Exact date-stamped variant should fall back to the undated entry.
	p, ok := LookupPricing("claude-opus-4-7-20260301")
	if !ok {
		t.Fatal("expected date-suffix fallback to hit claude-opus-4-7")
	}
	base, _ := LookupPricing("claude-opus-4-7")
	if p != base {
		t.Errorf("date-suffix lookup returned different pricing than base: %+v vs %+v", p, base)
	}
}

func TestLoadPricing_Override(t *testing.T) {
	t.Cleanup(func() { _, _ = LoadPricing("") })

	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.json")
	overrides := `{
		"claude-future-model": {"input": 7, "output": 35, "cache_read": 0.7, "cache_write_5m": 8.75, "cache_write_1h": 14},
		"claude-opus-4-7":     {"input": 6, "output": 30, "cache_read": 0.6, "cache_write_5m": 7.5,  "cache_write_1h": 12}
	}`
	if err := os.WriteFile(path, []byte(overrides), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := LoadPricing(path)
	if err != nil {
		t.Fatalf("LoadPricing failed: %v", err)
	}
	if source != path {
		t.Errorf("source = %q, want %q", source, path)
	}

	// New model is now resolvable.
	if p, ok := LookupPricing("claude-future-model"); !ok || p.Input != 7 {
		t.Errorf("claude-future-model not loaded: %+v ok=%v", p, ok)
	}
	// Existing model was overridden.
	p, ok := LookupPricing("claude-opus-4-7")
	if !ok || p.Input != 6 {
		t.Errorf("claude-opus-4-7 override not applied: %+v ok=%v", p, ok)
	}
	// Untouched models still fall back to defaults.
	if p, ok := LookupPricing("claude-haiku-4-5"); !ok || p.Input != 1 {
		t.Errorf("claude-haiku-4-5 should remain at $1 input: %+v ok=%v", p, ok)
	}
}

func TestLoadPricing_Missing(t *testing.T) {
	t.Cleanup(func() { _, _ = LoadPricing("") })

	// A non-empty path that does not exist is an error.
	if _, err := LoadPricing(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Error("expected error for missing explicit pricing file")
	}

	// Empty path with no default file present should silently use embedded.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source, err := LoadPricing("")
	if err != nil {
		t.Fatalf("empty path should not error when no default file exists: %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want embedded", source)
	}
}

func TestLoadPricing_Malformed(t *testing.T) {
	t.Cleanup(func() { _, _ = LoadPricing("") })
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPricing(path); err == nil {
		t.Error("expected error for malformed pricing JSON")
	}
}

func TestDataResidencyMultiplier(t *testing.T) {
	if got := DataResidencyMultiplier("us-only"); got != 1.1 {
		t.Errorf("us-only → %f, want 1.1", got)
	}
	if got := DataResidencyMultiplier(""); got != 1.0 {
		t.Errorf("empty → %f, want 1.0", got)
	}
	if got := DataResidencyMultiplier("global"); got != 1.0 {
		t.Errorf("global → %f, want 1.0", got)
	}
}

func TestWebSearchCost(t *testing.T) {
	if got := WebSearchCost(1000); got != 10.0 {
		t.Errorf("1000 searches → $%f, want $10", got)
	}
	if got := WebSearchCost(1); got != 0.01 {
		t.Errorf("1 search → $%f, want $0.01", got)
	}
	if got := WebSearchCost(0); got != 0 {
		t.Errorf("0 searches → $%f, want $0", got)
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

// --------------- context & memory tests ---------------

func TestExtractSystemPromptSize(t *testing.T) {
	req := `{"system":[{"type":"text","text":"You are Claude."},{"type":"text","text":"Memory content here."}],"messages":[]}`
	got := extractSystemPromptSize(req)
	if got == 0 {
		t.Error("expected non-zero system prompt size")
	}
	// Empty system
	if extractSystemPromptSize(`{"messages":[]}`) != 0 {
		t.Error("expected 0 for missing system field")
	}
}

func TestExtractMemoryFiles(t *testing.T) {
	req := `{"system":[{"type":"text","text":"Content from .claude/agents/expert.md and .claude/skills/review/SKILL.md loaded"}],"messages":[]}`
	files := extractMemoryFiles(req)
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(files), files)
	}
	if files[0] != ".claude/agents/expert.md" {
		t.Errorf("files[0] = %q, want .claude/agents/expert.md", files[0])
	}
	if files[1] != ".claude/skills/review/SKILL.md" {
		t.Errorf("files[1] = %q, want .claude/skills/review/SKILL.md", files[1])
	}
	// Deduplication
	req2 := `{"system":[{"type":"text","text":".claude/a.md and .claude/a.md again"}],"messages":[]}`
	if len(extractMemoryFiles(req2)) != 1 {
		t.Error("expected deduplication")
	}
}

func TestExtractResponse_SSE_MemoryOps(t *testing.T) {
	sse := lines(
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"Read"}}`,
		"event: content_block_delta",
		fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"%s"}}`,
			`{\"file_path\":\"/home/user/.claude/agents/expert.md\"}`),
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
	)
	rf := ExtractResponse(sse)
	if len(rf.MemoryOps) != 1 {
		t.Fatalf("got %d memory ops, want 1", len(rf.MemoryOps))
	}
	if rf.MemoryOps[0].Type != "read" {
		t.Errorf("op type = %q, want read", rf.MemoryOps[0].Type)
	}
	if !strings.Contains(rf.MemoryOps[0].Path, ".claude/") {
		t.Errorf("path = %q, want .claude/ path", rf.MemoryOps[0].Path)
	}
}

func TestExtractResponse_JSON_MemoryOps(t *testing.T) {
	resp := `{"content":[{"type":"tool_use","name":"Write","id":"t1","input":{"file_path":"/home/user/.claude/memory.md","content":"notes"}},{"type":"tool_use","name":"Read","id":"t2","input":{"file_path":"/home/user/code/main.go"}}]}`
	rf := ExtractResponse(resp)
	if len(rf.MemoryOps) != 1 {
		t.Fatalf("got %d memory ops, want 1 (only .claude/ paths)", len(rf.MemoryOps))
	}
	if rf.MemoryOps[0].Type != "write" {
		t.Errorf("op type = %q, want write", rf.MemoryOps[0].Type)
	}
}

func TestAnalyze_ContextAndMemory(t *testing.T) {
	now := time.Now()
	traces := []logger.Trace{
		{
			Timestamp: now, SessionID: "s1", Model: "claude-sonnet-4-5-20241022",
			InputTokens: 1000, OutputTokens: 200,
			Request:  `{"max_tokens":16000,"system":[{"type":"text","text":"Content from .claude/agents/expert.md"}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"go"}]}`,
			Response: `{"content":[{"type":"text","text":"hello"}]}`,
		},
		{
			Timestamp: now.Add(time.Minute), SessionID: "s1", Model: "claude-sonnet-4-5-20241022",
			InputTokens: 2000, OutputTokens: 300,
			Request:  `{"max_tokens":16000,"system":[{"type":"text","text":"Content from .claude/agents/expert.md"}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"go"},{"role":"assistant","content":"done"},{"role":"user","content":"next"}]}`,
			Response: `{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{"file_path":"/home/user/.claude/agents/expert.md"}}]}`,
		},
		{
			// Compaction: message count drops from 5 to 3
			Timestamp: now.Add(2 * time.Minute), SessionID: "s1", Model: "claude-sonnet-4-5-20241022",
			InputTokens: 800, OutputTokens: 100,
			Request:  `{"max_tokens":16000,"system":[{"type":"text","text":"Compacted"}],"messages":[{"role":"user","content":"summary"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue"}]}`,
			Response: `{"content":[{"type":"text","text":"ok"}]}`,
		},
	}

	report := Analyze(traces, []string{"test.jsonl"})

	// Context checks
	if report.Context.MaxMessageCount != 5 {
		t.Errorf("max message count = %d, want 5", report.Context.MaxMessageCount)
	}
	if report.Context.CompactionEvents != 1 {
		t.Errorf("compaction events = %d, want 1", report.Context.CompactionEvents)
	}
	if report.Context.AvgMessageCount < 3.0 {
		t.Errorf("avg message count = %.1f, want >= 3.0", report.Context.AvgMessageCount)
	}
	if report.Context.ContextGrowthRate < 1.0 {
		t.Errorf("growth rate = %.1f, want >= 1.0", report.Context.ContextGrowthRate)
	}

	// Compaction flag on message
	if !report.Messages[2].IsCompactionEvent {
		t.Error("msg[2] should be a compaction event")
	}
	if report.Messages[0].IsCompactionEvent || report.Messages[1].IsCompactionEvent {
		t.Error("msg[0] and msg[1] should not be compaction events")
	}

	// Memory checks
	if report.Memory.TotalRecalls != 1 {
		t.Errorf("total recalls = %d, want 1", report.Memory.TotalRecalls)
	}
	if report.Memory.TotalWrites != 0 {
		t.Errorf("total writes = %d, want 0", report.Memory.TotalWrites)
	}
	if report.Memory.UniqueFilesAccessed != 1 {
		t.Errorf("unique files = %d, want 1", report.Memory.UniqueFilesAccessed)
	}

	// Memory files loaded from system prompt
	if len(report.Messages[0].MemoryFilesLoaded) != 1 {
		t.Errorf("msg[0] memory files = %d, want 1", len(report.Messages[0].MemoryFilesLoaded))
	}
	// Compacted message has no .claude/ paths
	if len(report.Messages[2].MemoryFilesLoaded) != 0 {
		t.Errorf("msg[2] memory files = %d, want 0", len(report.Messages[2].MemoryFilesLoaded))
	}
}

func TestAnalyze_PricingDimensions(t *testing.T) {
	now := time.Now()
	traces := []logger.Trace{
		{
			// Baseline: standard request, no cache, no server tools, no residency.
			Timestamp:    now,
			SessionID:    "s1",
			Model:        "claude-opus-4-7",
			InputTokens:  1_000_000,
			OutputTokens: 0,
			Request:      `{"model":"claude-opus-4-7","messages":[]}`,
			Response:     `{"content":[],"usage":{"input_tokens":1000000}}`,
		},
		{
			// US-only residency → 1.1x multiplier on token cost.
			Timestamp:    now.Add(time.Second),
			SessionID:    "s1",
			Model:        "claude-opus-4-7",
			InputTokens:  1_000_000,
			OutputTokens: 0,
			InferenceGeo: "us-only",
			Request:      `{"model":"claude-opus-4-7","inference_geo":"us-only","messages":[]}`,
			Response:     `{"content":[],"usage":{"input_tokens":1000000}}`,
		},
		{
			// Cache creation with per-TTL split: 1M tokens 5m + 1M tokens 1h.
			Timestamp:             now.Add(2 * time.Second),
			SessionID:             "s1",
			Model:                 "claude-opus-4-7",
			CacheCreation5mTokens: 1_000_000,
			CacheCreation1hTokens: 1_000_000,
			Request:               `{"model":"claude-opus-4-7","messages":[]}`,
			Response:              `{"content":[],"usage":{"cache_creation_input_tokens":2000000,"cache_creation":{"ephemeral_5m_input_tokens":1000000,"ephemeral_1h_input_tokens":1000000}}}`,
		},
		{
			// Web search surcharge: 1,000 requests → $10.00.
			Timestamp:         now.Add(3 * time.Second),
			SessionID:         "s1",
			Model:             "claude-opus-4-7",
			WebSearchRequests: 1000,
			Request:           `{"model":"claude-opus-4-7","messages":[]}`,
			Response:          `{"content":[],"usage":{"server_tool_use":{"web_search_requests":1000}}}`,
		},
	}

	report := Analyze(traces, []string{"test.jsonl"})
	if len(report.Messages) != 4 {
		t.Fatalf("got %d messages, want 4", len(report.Messages))
	}

	// msg[0]: 1M input at $5 = $5.00
	if diff := report.Messages[0].Cost - 5.00; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("msg[0] cost = $%f, want ~$5.00", report.Messages[0].Cost)
	}
	// msg[1]: 1M input at $5 * 1.1 = $5.50
	if diff := report.Messages[1].Cost - 5.50; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("msg[1] cost (us-only) = $%f, want ~$5.50", report.Messages[1].Cost)
	}
	if report.Messages[1].InferenceGeo != "us-only" {
		t.Errorf("msg[1] InferenceGeo = %q, want us-only", report.Messages[1].InferenceGeo)
	}
	// msg[2]: 1M × $6.25 (5m) + 1M × $10.00 (1h) = $16.25
	if diff := report.Messages[2].Cost - 16.25; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("msg[2] cost (cache split) = $%f, want ~$16.25", report.Messages[2].Cost)
	}
	// msg[3]: 1000 web-search requests at $10/1000 = $10.00
	if diff := report.Messages[3].Cost - 10.00; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("msg[3] cost (web search) = $%f, want ~$10.00", report.Messages[3].Cost)
	}

	// CostReport summaries.
	if report.Cost.DataResidencyMessages != 1 {
		t.Errorf("DataResidencyMessages = %d, want 1", report.Cost.DataResidencyMessages)
	}
	if diff := report.Cost.DataResidencyAdjustment - 0.50; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("DataResidencyAdjustment = $%f, want ~$0.50", report.Cost.DataResidencyAdjustment)
	}
	if report.Cost.WebSearchRequestsTotal != 1000 {
		t.Errorf("WebSearchRequestsTotal = %d, want 1000", report.Cost.WebSearchRequestsTotal)
	}
	if diff := report.Cost.WebSearchCostTotal - 10.00; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("WebSearchCostTotal = $%f, want ~$10.00", report.Cost.WebSearchCostTotal)
	}

	// ModelCost aggregates the per-TTL cache breakdown.
	if len(report.Cost.ByModel) != 1 {
		t.Fatalf("got %d model rows, want 1", len(report.Cost.ByModel))
	}
	mc := report.Cost.ByModel[0]
	if mc.CacheCreation5mTokens != 1_000_000 || mc.CacheCreation1hTokens != 1_000_000 {
		t.Errorf("cache tokens = 5m=%d 1h=%d, want 1M/1M", mc.CacheCreation5mTokens, mc.CacheCreation1hTokens)
	}
	if diff := mc.CacheCreation5mCost - 6.25; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("5m cache cost = $%f, want $6.25", mc.CacheCreation5mCost)
	}
	if diff := mc.CacheCreation1hCost - 10.00; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("1h cache cost = $%f, want $10.00", mc.CacheCreation1hCost)
	}
}

// helper to join lines with newlines
func lines(ss ...string) string {
	return strings.Join(ss, "\n") + "\n"
}
