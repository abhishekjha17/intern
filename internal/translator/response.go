package translator

import (
	"encoding/json"

	"github.com/abhishekjha17/intern/internal/models"
)

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

// OllamaToAnthropic converts an Ollama/OpenAI response to Anthropic format.
// Handles both text-only and tool_calls responses.
func OllamaToAnthropic(resp models.OllamaResponse) models.AnthropicResponse {
	var content []models.ContentBlock
	var stopReason string

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		stopReason = mapFinishReason(choice.FinishReason)

		// Add text content block if there's text
		if choice.Message.Content != "" {
			content = append(content, models.ContentBlock{
				Type: "text",
				Text: choice.Message.Content,
			})
		}

		// Convert tool_calls to tool_use content blocks
		for _, tc := range choice.Message.ToolCalls {
			// Parse arguments JSON string into raw JSON for the Input field
			var inputRaw json.RawMessage
			if tc.Function.Arguments != "" {
				inputRaw = json.RawMessage(tc.Function.Arguments)
			} else {
				inputRaw = json.RawMessage("{}")
			}

			content = append(content, models.ContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: inputRaw,
			})
		}

		// Ensure at least one content block
		if len(content) == 0 {
			content = []models.ContentBlock{
				{Type: "text", Text: ""},
			}
		}
	}

	return models.AnthropicResponse{
		ID:         "msg_" + resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		Content:    content,
		StopReason: stopReason,
		Usage: models.AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
}
