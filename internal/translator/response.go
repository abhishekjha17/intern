package translator

import (
	"github.com/abhishekjha17/intern/internal/models"
)

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// OllamaToAnthropic converts an Ollama/OpenAI response to Anthropic format.
func OllamaToAnthropic(resp models.OllamaResponse) models.AnthropicResponse {
	var content []models.ContentBlock
	var stopReason string

	if len(resp.Choices) > 0 {
		content = []models.ContentBlock{
			{Type: "text", Text: resp.Choices[0].Message.Content},
		}
		stopReason = mapFinishReason(resp.Choices[0].FinishReason)
	}

	return models.AnthropicResponse{
		ID:       "msg_" + resp.ID,
		Type:     "message",
		Role:     "assistant",
		Model:    resp.Model,
		Content:  content,
		StopReason: stopReason,
		Usage: models.AnthropicUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
}
