package models

// --- Anthropic Request Types ---

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AnthropicRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
	System    string    `json:"system,omitempty"`
	Tools     interface{} `json:"tools,omitempty"`
}

// --- Anthropic Response Types ---

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type AnthropicResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []ContentBlock `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        AnthropicUsage `json:"usage"`
}

// --- Anthropic Streaming Types ---

type StreamDelta struct {
	Type         string  `json:"type,omitempty"`
	Text         string  `json:"text,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// --- Ollama/OpenAI Request Types ---

type OllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OllamaRequest struct {
	Model     string          `json:"model"`
	Messages  []OllamaMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Stream    bool            `json:"stream"`
}

// --- Ollama/OpenAI Response Types ---

type OllamaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type OllamaChoice struct {
	Index        int           `json:"index"`
	Message      OllamaMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type OllamaResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []OllamaChoice `json:"choices"`
	Usage   OllamaUsage    `json:"usage"`
}

// --- Ollama/OpenAI Streaming Types ---

type OllamaStreamChoice struct {
	Index        int           `json:"index"`
	Delta        OllamaMessage `json:"delta"`
	FinishReason *string       `json:"finish_reason"`
}

type OllamaStreamChunk struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []OllamaStreamChoice  `json:"choices"`
}
