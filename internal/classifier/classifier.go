package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/abhishekjha17/intern/internal/models"
)

const classifierTimeout = 2 * time.Second

const systemPrompt = `You are a routing classifier. Decide whether a user's request can be handled by a small local language model or needs a powerful cloud model.

Reply with exactly one word: LOCAL or CLOUD.

Route LOCAL when the request is:
- Simple code generation (boilerplate, regex, small functions)
- Text formatting or rewriting
- Simple Q&A with common knowledge
- Summarization of provided text
- Simple math or logic

Route CLOUD when the request is:
- Complex reasoning or multi-step analysis
- Tasks requiring deep domain expertise
- Long-form creative writing
- Tasks involving tool use or function calling
- Complex code architecture or debugging
- Anything ambiguous or unclear

If unsure, reply CLOUD.`

type Classifier struct {
	ollamaURL string
	model     string
	client    *http.Client
}

func New(ollamaURL, model string) *Classifier {
	return &Classifier{
		ollamaURL: ollamaURL,
		model:     model,
		client:    &http.Client{Timeout: classifierTimeout},
	}
}

func (c *Classifier) Classify(req models.AnthropicRequest) string {
	// Short-circuit: tool use always goes to cloud
	if req.Tools != nil {
		return "CLOUD"
	}

	// Extract last user message
	var lastPrompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastPrompt = req.Messages[i].Content
			break
		}
	}
	if lastPrompt == "" {
		return "CLOUD"
	}

	// Build classifier request to Ollama
	ollamaReq := models.OllamaRequest{
		Model: c.model,
		Messages: []models.OllamaMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: lastPrompt},
		},
		MaxTokens: 5,
		Stream:    false,
	}

	body, err := json.Marshal(ollamaReq)
	if err != nil {
		log.Printf("classifier: marshal error: %v", err)
		return "CLOUD"
	}

	ctx, cancel := context.WithTimeout(context.Background(), classifierTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.ollamaURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		log.Printf("classifier: request error: %v", err)
		return "CLOUD"
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		log.Printf("classifier: ollama unreachable: %v, falling back to CLOUD", err)
		return "CLOUD"
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("classifier: read error: %v", err)
		return "CLOUD"
	}

	var ollamaResp models.OllamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		log.Printf("classifier: unmarshal error: %v", err)
		return "CLOUD"
	}

	if len(ollamaResp.Choices) == 0 {
		return "CLOUD"
	}

	decision := strings.TrimSpace(strings.ToUpper(ollamaResp.Choices[0].Message.Content))
	if decision == "LOCAL" {
		log.Printf("classifier: decision=LOCAL for prompt: %.80s...", lastPrompt)
		return "LOCAL"
	}

	log.Printf("classifier: decision=CLOUD for prompt: %.80s...", lastPrompt)
	return "CLOUD"
}
