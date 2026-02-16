package models

import (
	"encoding/json"
	"strings"
)

// --- Anthropic Request Types ---

type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string OR []ContentBlock
}

// ExtractText pulls plain text from Content, handling both string and array forms.
func (m Message) ExtractText() string {
	if len(m.Content) == 0 {
		return ""
	}

	// Try string first
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}

	// Try array of content blocks
	var blocks []ContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

// ParseContentBlocks parses Content as an array of ContentBlock.
// Returns nil if Content is a plain string.
func (m Message) ParseContentBlocks() []ContentBlock {
	var blocks []ContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		return blocks
	}
	return nil
}

// AnthropicTool is a tool definition in Anthropic format.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// tool_use fields
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result fields
	ToolUseID        string          `json:"tool_use_id,omitempty"`
	ToolResultContent json.RawMessage `json:"content,omitempty"` // string or []ContentBlock for tool_result
}

type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type AnthropicRequest struct {
	Model     string          `json:"model"`
	Messages  []Message       `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	System    json.RawMessage `json:"system,omitempty"`
	Tools     json.RawMessage `json:"tools,omitempty"`
}

// HasTools returns true if the request contains tool definitions.
func (r AnthropicRequest) HasTools() bool {
	return len(r.Tools) > 0 && string(r.Tools) != "null"
}

// ParseTools parses the raw Tools field into structured AnthropicTool slice.
func (r AnthropicRequest) ParseTools() []AnthropicTool {
	var tools []AnthropicTool
	json.Unmarshal(r.Tools, &tools)
	return tools
}

// ExtractSystemText extracts plain text from the System field (handles string or array).
func (r AnthropicRequest) ExtractSystemText() string {
	if len(r.System) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(r.System, &s); err == nil {
		return s
	}
	// System can be array of {type:"text", text:"..."}
	var blocks []ContentBlock
	if err := json.Unmarshal(r.System, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
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
	Type         string          `json:"type,omitempty"`
	Text         string          `json:"text,omitempty"`
	PartialJSON  string          `json:"partial_json,omitempty"`
	StopReason   string          `json:"stop_reason,omitempty"`
	StopSequence *string         `json:"stop_sequence,omitempty"`
}

// --- Ollama/OpenAI Request Types ---

type OllamaFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OllamaToolCall struct {
	ID       string             `json:"id"`
	Index    int                `json:"index"`
	Type     string             `json:"type"`
	Function OllamaFunctionCall `json:"function"`
}

type OllamaMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []OllamaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type OllamaFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type OllamaToolDef struct {
	Type     string            `json:"type"`
	Function OllamaFunctionDef `json:"function"`
}

type OllamaRequest struct {
	Model     string          `json:"model"`
	Messages  []OllamaMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Stream    bool            `json:"stream"`
	Tools     []OllamaToolDef `json:"tools,omitempty"`
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
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []OllamaStreamChoice `json:"choices"`
}
