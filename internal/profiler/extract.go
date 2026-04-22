package profiler

import (
	"bufio"
	"encoding/json"
	"regexp"
	"strings"
)

// ResponseFields holds all fields extracted from a raw response in a single pass.
type ResponseFields struct {
	Blocks           []BlockInfo
	HasThinkingBlock bool
	HasThinkingText  bool

	// CacheCreation is the total (5m + 1h) for legacy callers.
	// CacheCreation5m / CacheCreation1h split the total when the API provides
	// a per-TTL breakdown; when only the top-level sum is available the whole
	// amount is attributed to the 5-minute bucket (Anthropic's default cache TTL).
	CacheCreation   int
	CacheCreation5m int
	CacheCreation1h int

	// Server-side tool invocation counts (metered separately from tokens).
	WebSearchRequests     int
	CodeExecutionRequests int

	BashCommands []string
	MemoryOps    []MemoryOp
}

// ExtractResponse parses a raw response (SSE or JSON) in a single pass,
// returning all extracted fields at once.
func ExtractResponse(response string) ResponseFields {
	if strings.HasPrefix(response, "event:") {
		return extractResponseSSE(response)
	}
	return extractResponseJSON(response)
}

func extractResponseSSE(response string) ResponseFields {
	var rf ResponseFields

	// Track tool_use blocks for input extraction.
	type toolBlock struct {
		name    string
		jsonBuf strings.Builder
	}
	toolBlocks := map[int]*toolBlock{}

	type usageShape struct {
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheCreation            *struct {
			Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation,omitempty"`
		ServerToolUse *struct {
			WebSearchRequests     int `json:"web_search_requests"`
			CodeExecutionRequests int `json:"code_execution_requests"`
		} `json:"server_tool_use,omitempty"`
	}

	// cacheCreationSeen tracks whether any earlier event already reported a
	// cache_creation breakdown; once set we don't let a later message_delta
	// overwrite the initial message_start values. This mirrors the
	// roundtripper's first-writer-wins policy so the persisted trace and the
	// re-parsed response agree on cache token attribution.
	var cacheCreationSeen bool
	applyUsage := func(u usageShape, allowCacheOverwrite bool) {
		if allowCacheOverwrite || !cacheCreationSeen {
			if u.CacheCreation != nil {
				rf.CacheCreation5m = u.CacheCreation.Ephemeral5mInputTokens
				rf.CacheCreation1h = u.CacheCreation.Ephemeral1hInputTokens
				rf.CacheCreation = rf.CacheCreation5m + rf.CacheCreation1h
				cacheCreationSeen = true
			} else if u.CacheCreationInputTokens > 0 {
				rf.CacheCreation = u.CacheCreationInputTokens
				rf.CacheCreation5m = u.CacheCreationInputTokens
				cacheCreationSeen = true
			}
		}
		if u.ServerToolUse != nil {
			rf.WebSearchRequests = u.ServerToolUse.WebSearchRequests
			rf.CodeExecutionRequests = u.ServerToolUse.CodeExecutionRequests
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(response))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var evt struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Message struct {
				Usage usageShape `json:"usage"`
			} `json:"message"`
			Usage usageShape `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &evt) != nil {
			continue
		}

		switch evt.Type {
		case "message_start":
			applyUsage(evt.Message.Usage, true)
		case "message_delta":
			// server_tool_use counts always update; cache_creation only fills in
			// when message_start didn't already provide a breakdown.
			applyUsage(evt.Usage, false)

		case "content_block_start":
			rf.Blocks = append(rf.Blocks, BlockInfo{
				Type: evt.ContentBlock.Type,
				Name: evt.ContentBlock.Name,
			})
			if evt.ContentBlock.Type == "thinking" {
				rf.HasThinkingBlock = true
			}
			if evt.ContentBlock.Type == "tool_use" {
				switch evt.ContentBlock.Name {
				case "Bash", "Read", "Write", "Edit":
					toolBlocks[evt.Index] = &toolBlock{name: evt.ContentBlock.Name}
				}
			}

		case "content_block_delta":
			if evt.Delta.Type == "thinking_delta" {
				rf.HasThinkingText = true
			}
			if tb, ok := toolBlocks[evt.Index]; ok && evt.Delta.Type == "input_json_delta" {
				tb.jsonBuf.WriteString(evt.Delta.PartialJSON)
			}

		case "content_block_stop":
			if tb, ok := toolBlocks[evt.Index]; ok {
				var input struct {
					Command  string `json:"command"`
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal([]byte(tb.jsonBuf.String()), &input) == nil {
					if tb.name == "Bash" && input.Command != "" {
						rf.BashCommands = append(rf.BashCommands, input.Command)
					}
					if input.FilePath != "" && strings.Contains(input.FilePath, ".claude/") {
						opType := "read"
						if tb.name == "Write" || tb.name == "Edit" {
							opType = "write"
						}
						rf.MemoryOps = append(rf.MemoryOps, MemoryOp{Type: opType, Path: input.FilePath})
					}
				}
				delete(toolBlocks, evt.Index)
			}
		}
	}
	return rf
}

func extractResponseJSON(response string) ResponseFields {
	var rf ResponseFields
	var resp struct {
		Content []struct {
			Type     string `json:"type"`
			Name     string `json:"name"`
			Thinking string `json:"thinking"`
			Input    struct {
				Command  string `json:"command"`
				FilePath string `json:"file_path"`
			} `json:"input"`
		} `json:"content"`
		Usage struct {
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
				Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation,omitempty"`
			ServerToolUse *struct {
				WebSearchRequests     int `json:"web_search_requests"`
				CodeExecutionRequests int `json:"code_execution_requests"`
			} `json:"server_tool_use,omitempty"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(response), &resp) != nil {
		return rf
	}

	if resp.Usage.CacheCreation != nil {
		rf.CacheCreation5m = resp.Usage.CacheCreation.Ephemeral5mInputTokens
		rf.CacheCreation1h = resp.Usage.CacheCreation.Ephemeral1hInputTokens
		rf.CacheCreation = rf.CacheCreation5m + rf.CacheCreation1h
	} else {
		rf.CacheCreation = resp.Usage.CacheCreationInputTokens
		rf.CacheCreation5m = resp.Usage.CacheCreationInputTokens
	}
	if resp.Usage.ServerToolUse != nil {
		rf.WebSearchRequests = resp.Usage.ServerToolUse.WebSearchRequests
		rf.CodeExecutionRequests = resp.Usage.ServerToolUse.CodeExecutionRequests
	}
	rf.Blocks = make([]BlockInfo, len(resp.Content))
	for i, c := range resp.Content {
		rf.Blocks[i] = BlockInfo{Type: c.Type, Name: c.Name}
		if c.Type == "thinking" {
			rf.HasThinkingBlock = true
			if c.Thinking != "" {
				rf.HasThinkingText = true
			}
		}
		if c.Type == "tool_use" {
			if c.Name == "Bash" && c.Input.Command != "" {
				rf.BashCommands = append(rf.BashCommands, c.Input.Command)
			}
			if c.Input.FilePath != "" && strings.Contains(c.Input.FilePath, ".claude/") {
				opType := "read"
				if c.Name == "Write" || c.Name == "Edit" {
					opType = "write"
				}
				rf.MemoryOps = append(rf.MemoryOps, MemoryOp{Type: opType, Path: c.Input.FilePath})
			}
		}
	}
	return rf
}

// --- Helpers used by Analyze and tests ---

// extractToolsCalled returns the tool names from content blocks that are
// tool_use or server_tool_use.
func extractToolsCalled(blocks []BlockInfo) []string {
	var tools []string
	for _, b := range blocks {
		if b.Type == "tool_use" || b.Type == "server_tool_use" {
			tools = append(tools, b.Name)
		}
	}
	return tools
}

// classifyOutputType categorizes the response by its content block composition.
func classifyOutputType(blocks []BlockInfo) string {
	hasText := false
	hasTool := false
	for _, b := range blocks {
		switch b.Type {
		case "text":
			hasText = true
		case "tool_use", "server_tool_use":
			hasTool = true
		}
	}
	switch {
	case hasText && hasTool:
		return "mixed"
	case hasTool:
		return "tool_calls_only"
	case hasText:
		return "text_only"
	default:
		return "empty"
	}
}

// extractMaxTokens parses max_tokens from the raw request JSON.
func extractMaxTokens(request string) int {
	var req struct {
		MaxTokens int `json:"max_tokens"`
	}
	if json.Unmarshal([]byte(request), &req) != nil {
		return 0
	}
	return req.MaxTokens
}

// extractHasToolResult checks if the last user message in the request
// contains tool_result content blocks.
func extractHasToolResult(request string) bool {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(request), &req) != nil {
		return false
	}

	// Find last user message.
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.Messages[i].Content, &blocks) != nil {
			return false
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return true
			}
		}
		return false
	}
	return false
}

// extractMessageCount returns the number of messages in the request.
func extractMessageCount(request string) int {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal([]byte(request), &req) != nil {
		return 0
	}
	return len(req.Messages)
}

// extractSystemPromptSize returns the byte length of the raw system field.
func extractSystemPromptSize(request string) int {
	var req struct {
		System json.RawMessage `json:"system"`
	}
	if json.Unmarshal([]byte(request), &req) != nil || req.System == nil {
		return 0
	}
	return len(req.System)
}

// extractInferenceGeo returns the value of the request's inference_geo field,
// or the empty string when absent. "us-only" triggers the 1.1x data-residency
// multiplier; any other value uses standard pricing.
func extractInferenceGeo(request string) string {
	var req struct {
		InferenceGeo string `json:"inference_geo"`
	}
	if json.Unmarshal([]byte(request), &req) != nil {
		return ""
	}
	return req.InferenceGeo
}

var memoryPathRe = regexp.MustCompile(`\.claude/[\w/._-]+`)

// extractMemoryFiles scans the system prompt for .claude/ path references.
func extractMemoryFiles(request string) []string {
	var req struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if json.Unmarshal([]byte(request), &req) != nil {
		return nil
	}
	seen := map[string]bool{}
	var files []string
	for _, block := range req.System {
		for _, match := range memoryPathRe.FindAllString(block.Text, -1) {
			if !seen[match] {
				seen[match] = true
				files = append(files, match)
			}
		}
	}
	return files
}
