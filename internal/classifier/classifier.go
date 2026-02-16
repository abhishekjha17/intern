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

const classifierTimeout = 5 * time.Second

const systemPrompt = `Classify the user's request as LOCAL or CLOUD. Output ONLY the single word LOCAL or CLOUD, nothing else.

LOCAL = simple tasks: basic code, formatting, short Q&A, summarization, simple math.
CLOUD = complex tasks: deep reasoning, multi-step analysis, expert knowledge, long creative writing, architecture design.

Default: CLOUD`

type Classifier struct {
	ollamaURL string
	model     string
	client    *http.Client
}

func New(ollamaURL, model string) *Classifier {
	return &Classifier{
		ollamaURL: ollamaURL,
		model:     model,
		client:    &http.Client{},
	}
}

// classifierRequest adds temperature control on top of the standard fields.
type classifierRequest struct {
	Model       string                 `json:"model"`
	Messages    []models.OllamaMessage `json:"messages"`
	MaxTokens   int                    `json:"max_tokens,omitempty"`
	Stream      bool                   `json:"stream"`
	Temperature float64                `json:"temperature"`
}

func (c *Classifier) Classify(req models.AnthropicRequest) string {
	// Check if this is a multi-turn tool conversation (contains tool_result blocks).
	// If so, route locally — the conversation was already started locally.
	if hasToolResults(req) {
		log.Printf("classifier: tool_result messages present, routing LOCAL (continuing tool conversation)")
		return "LOCAL"
	}

	// Extract last user message text (handles both string and content block array)
	var lastPrompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastPrompt = req.Messages[i].ExtractText()
			break
		}
	}
	if lastPrompt == "" {
		return "CLOUD"
	}

	// Build zero-shot classifier request
	ollamaReq := classifierRequest{
		Model: c.model,
		Messages: []models.OllamaMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: lastPrompt},
		},
		MaxTokens:   3,
		Stream:      false,
		Temperature: 0,
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

	raw := ollamaResp.Choices[0].Message.Content
	decision := strings.ToUpper(strings.TrimSpace(raw))
	log.Printf("classifier: raw=%q prompt=%.80s", raw, lastPrompt)

	// Fuzzy match: check if response contains LOCAL or CLOUD
	if strings.Contains(decision, "LOCAL") && !strings.Contains(decision, "CLOUD") {
		return "LOCAL"
	}
	if strings.Contains(decision, "CLOUD") {
		return "CLOUD"
	}

	log.Printf("classifier: unrecognized response %q, defaulting to CLOUD", raw)
	return "CLOUD"
}

// hasToolResults checks if any message in the conversation contains tool_result blocks.
// This indicates a multi-turn tool conversation that should continue locally.
func hasToolResults(req models.AnthropicRequest) bool {
	for _, msg := range req.Messages {
		blocks := msg.ParseContentBlocks()
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return true
			}
		}
	}
	return false
}
