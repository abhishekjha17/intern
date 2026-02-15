package translator

import (
	"github.com/abhishekjha17/intern/internal/models"
)

// AnthropicToOllama converts an Anthropic-format request to an Ollama/OpenAI-compatible request.
func AnthropicToOllama(req models.AnthropicRequest, localModel string) models.OllamaRequest {
	var messages []models.OllamaMessage

	// Anthropic system field becomes a system-role message
	if req.System != "" {
		messages = append(messages, models.OllamaMessage{
			Role:    "system",
			Content: req.System,
		})
	}

	for _, msg := range req.Messages {
		messages = append(messages, models.OllamaMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return models.OllamaRequest{
		Model:     localModel,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
		Stream:    req.Stream,
	}
}
