package translator

import (
	"encoding/json"

	"github.com/abhishekjha17/intern/internal/models"
)

// AnthropicToOllama converts an Anthropic-format request to Ollama/OpenAI format,
// handling tool definitions, tool_use blocks, and tool_result messages.
func AnthropicToOllama(req models.AnthropicRequest, localModel string) models.OllamaRequest {
	var messages []models.OllamaMessage

	// System prompt → system message
	if sysText := req.ExtractSystemText(); sysText != "" {
		messages = append(messages, models.OllamaMessage{
			Role:    "system",
			Content: sysText,
		})
	}

	// Translate each message
	for _, msg := range req.Messages {
		translated := translateMessage(msg)
		messages = append(messages, translated...)
	}

	result := models.OllamaRequest{
		Model:     localModel,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
		Stream:    req.Stream,
	}

	// Translate tool definitions
	if req.HasTools() {
		result.Tools = translateToolDefs(req.ParseTools())
	}

	return result
}

// translateMessage converts a single Anthropic message into one or more Ollama messages.
// A single Anthropic message with tool_result blocks becomes multiple Ollama messages.
func translateMessage(msg models.Message) []models.OllamaMessage {
	// Try as plain string first
	var contentStr string
	if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
		return []models.OllamaMessage{{
			Role:    msg.Role,
			Content: contentStr,
		}}
	}

	// Parse as content block array
	blocks := msg.ParseContentBlocks()
	if blocks == nil {
		return []models.OllamaMessage{{
			Role:    msg.Role,
			Content: string(msg.Content),
		}}
	}

	var result []models.OllamaMessage
	var textParts []string
	var toolCalls []models.OllamaToolCall

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)

		case "tool_use":
			// Convert input (json.RawMessage) to arguments string
			args := "{}"
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			toolCalls = append(toolCalls, models.OllamaToolCall{
				ID:   block.ID,
				Type: "function",
				Function: models.OllamaFunctionCall{
					Name:      block.Name,
					Arguments: args,
				},
			})

		case "tool_result":
			// tool_result blocks become separate role:"tool" messages in OpenAI format.
			// First, flush any accumulated text + tool_calls for the current message.
			if len(textParts) > 0 || len(toolCalls) > 0 {
				m := models.OllamaMessage{
					Role:      msg.Role,
					Content:   joinText(textParts),
					ToolCalls: toolCalls,
				}
				result = append(result, m)
				textParts = nil
				toolCalls = nil
			}

			// Extract tool result content as string
			resultContent := extractToolResultContent(block)

			result = append(result, models.OllamaMessage{
				Role:       "tool",
				Content:    resultContent,
				ToolCallID: block.ToolUseID,
			})
		}
	}

	// Flush remaining text + tool_calls
	if len(textParts) > 0 || len(toolCalls) > 0 {
		m := models.OllamaMessage{
			Role:      msg.Role,
			Content:   joinText(textParts),
			ToolCalls: toolCalls,
		}
		result = append(result, m)
	}

	// If nothing was produced (e.g., empty content array), emit a placeholder
	if len(result) == 0 {
		result = append(result, models.OllamaMessage{
			Role:    msg.Role,
			Content: "",
		})
	}

	return result
}

// extractToolResultContent extracts a string from a tool_result block's content.
// Anthropic tool_result content can be:
// - A plain string in the "content" field
// - An array of content blocks in the "content" field
// - A plain string in the "text" field (simplified form)
// - Absent (empty result)
func extractToolResultContent(block models.ContentBlock) string {
	// Check the Text field first (simplified form)
	if block.Text != "" {
		return block.Text
	}

	// Check the ToolResultContent field (maps to JSON "content")
	if len(block.ToolResultContent) > 0 {
		// Try as plain string
		var s string
		if err := json.Unmarshal(block.ToolResultContent, &s); err == nil {
			return s
		}

		// Try as array of content blocks
		var blocks []models.ContentBlock
		if err := json.Unmarshal(block.ToolResultContent, &blocks); err == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			return joinText(parts)
		}

		// Fallback: return raw string representation
		return string(block.ToolResultContent)
	}

	return ""
}

// translateToolDefs converts Anthropic tool definitions to OpenAI/Ollama format.
func translateToolDefs(tools []models.AnthropicTool) []models.OllamaToolDef {
	var result []models.OllamaToolDef
	for _, t := range tools {
		result = append(result, models.OllamaToolDef{
			Type: "function",
			Function: models.OllamaFunctionDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return result
}

func joinText(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += "\n"
		}
		result += p
	}
	return result
}
