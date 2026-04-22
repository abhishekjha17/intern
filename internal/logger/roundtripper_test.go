package logger

import "testing"

func TestUsageFromSSE_ThinkingDelta(t *testing.T) {
	// Simulates a streaming response with thinking_delta events followed by text.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-6","id":"msg_test","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":50}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think about this "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step by step. First I need to consider the options."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EpMDClkIDBgC..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is my answer."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":200}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, content := UsageFromSSE(body)

	if usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 50 {
		t.Errorf("CacheReadInputTokens = %d, want 50", usage.CacheReadInputTokens)
	}
	if len(content) != 1 || content[0].Type != "thinking" {
		t.Fatalf("expected 1 thinking content block, got %d blocks", len(content))
	}

	wantThinking := "Let me think about this step by step. First I need to consider the options."
	if content[0].Thinking != wantThinking {
		t.Errorf("Thinking = %q, want %q", content[0].Thinking, wantThinking)
	}

	tokens := ThinkingTokensFromContent(content)
	if tokens == 0 {
		t.Error("ThinkingTokensFromContent returned 0, want > 0")
	}
	// 74 chars / 4 = 18 tokens
	if tokens != 18 {
		t.Errorf("ThinkingTokensFromContent = %d, want 18", tokens)
	}
}

func TestUsageFromSSE_NoThinking(t *testing.T) {
	// Simulates a streaming response with only text, no thinking blocks.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","id":"msg_test2","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":50,"output_tokens":5,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":30}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, content := UsageFromSSE(body)

	if usage.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", usage.InputTokens)
	}
	if usage.OutputTokens != 30 {
		t.Errorf("OutputTokens = %d, want 30", usage.OutputTokens)
	}
	if len(content) != 0 {
		t.Errorf("expected 0 content blocks, got %d", len(content))
	}
	if ThinkingTokensFromContent(content) != 0 {
		t.Error("ThinkingTokensFromContent should be 0 with no thinking blocks")
	}
}

func TestUsageFromSSE_RedactedThinking(t *testing.T) {
	// Simulates a thinking block with only signature_delta (no thinking text).
	// This matches the actual behavior seen in traces.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-6","id":"msg_test3","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":200,"output_tokens":45,"cache_read_input_tokens":100}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EpMDClkIDBgC..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"The answer is 42."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":150}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, content := UsageFromSSE(body)

	if usage.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", usage.OutputTokens)
	}
	// Redacted thinking has no thinking_delta events, so no content blocks accumulated.
	if len(content) != 0 {
		t.Errorf("expected 0 content blocks for redacted thinking, got %d", len(content))
	}
	if ThinkingTokensFromContent(content) != 0 {
		t.Errorf("ThinkingTokensFromContent should be 0 for redacted thinking")
	}
}

func TestUsageFromSSE_CacheOnlyInputTokensZero(t *testing.T) {
	// When all input tokens come from cache, input_tokens is 0 but
	// cache_read_input_tokens is non-zero. The parser must still extract usage.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","id":"msg_test4","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":0,"cache_creation_input_tokens":459,"cache_read_input_tokens":35355,"output_tokens":66}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Response"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":80}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, _ := UsageFromSSE(body)

	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 35355 {
		t.Errorf("CacheReadInputTokens = %d, want 35355", usage.CacheReadInputTokens)
	}
	if usage.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80", usage.OutputTokens)
	}
}

func TestUsageFromSSE_ToolUse(t *testing.T) {
	// Streaming response with tool_use block — verify usage is extracted correctly.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-6","id":"msg_test5","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":500,"output_tokens":50,"cache_read_input_tokens":38048}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01abc","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls -la\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":122}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, content := UsageFromSSE(body)

	if usage.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", usage.InputTokens)
	}
	if usage.OutputTokens != 122 {
		t.Errorf("OutputTokens = %d, want 122", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 38048 {
		t.Errorf("CacheReadInputTokens = %d, want 38048", usage.CacheReadInputTokens)
	}
	// tool_use blocks should not produce thinking content
	if len(content) != 0 {
		t.Errorf("expected 0 content blocks for tool_use, got %d", len(content))
	}
}

func TestUsageFromSSE_CacheCreationBreakdown(t *testing.T) {
	// Explicit 5m / 1h cache TTLs: the nested cache_creation object takes
	// precedence over the top-level cache_creation_input_tokens sum.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":10,"cache_creation_input_tokens":7000,"cache_creation":{"ephemeral_5m_input_tokens":5000,"ephemeral_1h_input_tokens":2000}}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}
`
	usage, _ := UsageFromSSE(body)
	if usage.CacheCreation == nil {
		t.Fatal("CacheCreation breakdown not captured")
	}
	if usage.CacheCreation.Ephemeral5mInputTokens != 5000 {
		t.Errorf("5m = %d, want 5000", usage.CacheCreation.Ephemeral5mInputTokens)
	}
	if usage.CacheCreation.Ephemeral1hInputTokens != 2000 {
		t.Errorf("1h = %d, want 2000", usage.CacheCreation.Ephemeral1hInputTokens)
	}

	fiveMin, oneHour := cacheCreationTokens(usage)
	if fiveMin != 5000 || oneHour != 2000 {
		t.Errorf("cacheCreationTokens = (%d, %d), want (5000, 2000)", fiveMin, oneHour)
	}
}

func TestUsageFromSSE_ServerToolUse(t *testing.T) {
	// server_tool_use counts may appear in both message_start (initial) and
	// message_delta (final). The delta value wins.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-7","usage":{"input_tokens":100,"output_tokens":10,"server_tool_use":{"web_search_requests":1,"code_execution_requests":0}}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50,"server_tool_use":{"web_search_requests":3,"code_execution_requests":2}}}
`
	usage, _ := UsageFromSSE(body)
	if usage.ServerToolUse == nil {
		t.Fatal("ServerToolUse not captured")
	}
	if usage.ServerToolUse.WebSearchRequests != 3 {
		t.Errorf("web_search = %d, want 3", usage.ServerToolUse.WebSearchRequests)
	}
	if usage.ServerToolUse.CodeExecutionRequests != 2 {
		t.Errorf("code_exec = %d, want 2", usage.ServerToolUse.CodeExecutionRequests)
	}
}

func TestCacheCreationTokens_SumOnlyFallback(t *testing.T) {
	// No explicit breakdown → attribute the whole sum to the 5m bucket,
	// since that's Anthropic's default TTL when cache_control is set without
	// an explicit ttl field.
	u := AnthropicUsage{CacheCreationInputTokens: 1234}
	fiveMin, oneHour := cacheCreationTokens(u)
	if fiveMin != 1234 || oneHour != 0 {
		t.Errorf("got (%d, %d), want (1234, 0)", fiveMin, oneHour)
	}
}

func TestUsageFromSSE_MixedThinkingTextToolUse(t *testing.T) {
	// Realistic response with thinking + text + tool_use blocks.
	body := `event: message_start
data: {"type":"message_start","message":{"model":"claude-opus-4-6","id":"msg_test6","type":"message","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":2,"output_tokens":45,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I need to check the git status first."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me check."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_01xyz","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"git status\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":277}}

event: message_stop
data: {"type":"message_stop"}
`
	usage, content := UsageFromSSE(body)

	if usage.InputTokens != 2 {
		t.Errorf("InputTokens = %d, want 2", usage.InputTokens)
	}
	if usage.OutputTokens != 277 {
		t.Errorf("OutputTokens = %d, want 277", usage.OutputTokens)
	}
	if len(content) != 1 || content[0].Type != "thinking" {
		t.Fatalf("expected 1 thinking block, got %d blocks", len(content))
	}
	if content[0].Thinking != "I need to check the git status first." {
		t.Errorf("Thinking = %q", content[0].Thinking)
	}
	// 38 chars / 4 = 9
	if got := ThinkingTokensFromContent(content); got != 9 {
		t.Errorf("ThinkingTokensFromContent = %d, want 9", got)
	}
}
