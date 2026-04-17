package profiler

import (
	"bufio"
	"encoding/json"
	"strings"
)

// ResponseFields holds all fields extracted from a raw response in a single pass.
type ResponseFields struct {
	Blocks          []BlockInfo
	HasThinkingBlock bool
	HasThinkingText  bool
	CacheCreation    int
	BashCommands     []string
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

	// Track Bash tool_use blocks for command extraction.
	type bashBlock struct {
		jsonBuf strings.Builder
	}
	bashBlocks := map[int]*bashBlock{}

	scanner := bufio.NewScanner(strings.NewReader(response))
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
				Usage struct {
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(data), &evt) != nil {
			continue
		}

		switch evt.Type {
		case "message_start":
			rf.CacheCreation = evt.Message.Usage.CacheCreationInputTokens

		case "content_block_start":
			rf.Blocks = append(rf.Blocks, BlockInfo{
				Type: evt.ContentBlock.Type,
				Name: evt.ContentBlock.Name,
			})
			if evt.ContentBlock.Type == "thinking" {
				rf.HasThinkingBlock = true
			}
			if evt.ContentBlock.Type == "tool_use" && evt.ContentBlock.Name == "Bash" {
				bashBlocks[evt.Index] = &bashBlock{}
			}

		case "content_block_delta":
			if evt.Delta.Type == "thinking_delta" {
				rf.HasThinkingText = true
			}
			if bb, ok := bashBlocks[evt.Index]; ok && evt.Delta.Type == "input_json_delta" {
				bb.jsonBuf.WriteString(evt.Delta.PartialJSON)
			}

		case "content_block_stop":
			if bb, ok := bashBlocks[evt.Index]; ok {
				var input struct {
					Command string `json:"command"`
				}
				if json.Unmarshal([]byte(bb.jsonBuf.String()), &input) == nil && input.Command != "" {
					rf.BashCommands = append(rf.BashCommands, input.Command)
				}
				delete(bashBlocks, evt.Index)
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
				Command string `json:"command"`
			} `json:"input"`
		} `json:"content"`
		Usage struct {
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(response), &resp) != nil {
		return rf
	}

	rf.CacheCreation = resp.Usage.CacheCreationInputTokens
	rf.Blocks = make([]BlockInfo, len(resp.Content))
	for i, c := range resp.Content {
		rf.Blocks[i] = BlockInfo{Type: c.Type, Name: c.Name}
		if c.Type == "thinking" {
			rf.HasThinkingBlock = true
			if c.Thinking != "" {
				rf.HasThinkingText = true
			}
		}
		if (c.Type == "tool_use") && c.Name == "Bash" && c.Input.Command != "" {
			rf.BashCommands = append(rf.BashCommands, c.Input.Command)
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
