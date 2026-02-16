package translator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/abhishekjha17/intern/internal/models"
)

// StreamOllamaToAnthropic reads Ollama's OpenAI-format SSE stream and writes
// Anthropic-format SSE events to the client.
// Handles both text deltas and tool_calls in the stream.
func StreamOllamaToAnthropic(w http.ResponseWriter, ollamaBody io.Reader, ollamaModel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Emit message_start
	writeSSE(w, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": models.AnthropicResponse{
			ID:      "msg_local",
			Type:    "message",
			Role:    "assistant",
			Model:   ollamaModel,
			Content: []models.ContentBlock{},
			Usage:   models.AnthropicUsage{},
		},
	})
	flusher.Flush()

	// Read Ollama SSE stream
	scanner := bufio.NewScanner(ollamaBody)
	var outputTokens int
	var blockIndex int        // tracks current content_block index
	textBlockStarted := false // whether we've emitted content_block_start for text
	stopReason := "end_turn"

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			break
		}

		var chunk models.OllamaStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Printf("stream translator: parse error: %v", err)
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		choice := chunk.Choices[0]

		// Handle text content
		text := choice.Delta.Content
		if text != "" {
			if !textBlockStarted {
				// Emit content_block_start for text
				writeSSE(w, "content_block_start", map[string]interface{}{
					"type":          "content_block_start",
					"index":         blockIndex,
					"content_block": models.ContentBlock{Type: "text", Text: ""},
				})
				flusher.Flush()
				textBlockStarted = true
			}

			outputTokens++
			writeSSE(w, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": models.StreamDelta{Type: "text_delta", Text: text},
			})
			flusher.Flush()
		}

		// Handle tool_calls in delta
		// Ollama sends tool_calls in the first delta chunk (all at once, not incrementally)
		if len(choice.Delta.ToolCalls) > 0 {
			// Close text block if it was open
			if textBlockStarted {
				writeSSE(w, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": blockIndex,
				})
				flusher.Flush()
				blockIndex++
				textBlockStarted = false
			}

			// Emit each tool call as a content block
			for _, tc := range choice.Delta.ToolCalls {
				// content_block_start with tool_use
				inputRaw := json.RawMessage("{}")
				writeSSE(w, "content_block_start", map[string]interface{}{
					"type":  "content_block_start",
					"index": blockIndex,
					"content_block": map[string]interface{}{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": json.RawMessage("{}"),
					},
				})
				flusher.Flush()

				// content_block_delta with input_json_delta
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				_ = inputRaw // suppress unused
				writeSSE(w, "content_block_delta", map[string]interface{}{
					"type":  "content_block_delta",
					"index": blockIndex,
					"delta": models.StreamDelta{Type: "input_json_delta", PartialJSON: args},
				})
				flusher.Flush()

				// content_block_stop
				writeSSE(w, "content_block_stop", map[string]interface{}{
					"type":  "content_block_stop",
					"index": blockIndex,
				})
				flusher.Flush()

				blockIndex++
			}

			stopReason = "tool_use"
		}

		// Check finish reason
		if choice.FinishReason != nil {
			fr := *choice.FinishReason
			if fr == "tool_calls" {
				stopReason = "tool_use"
			} else if fr == "length" {
				stopReason = "max_tokens"
			}
			break
		}
	}

	// Close text block if still open
	if textBlockStarted {
		writeSSE(w, "content_block_stop", map[string]interface{}{
			"type":  "content_block_stop",
			"index": blockIndex,
		})
		flusher.Flush()
	}

	// Emit message_delta with stop reason
	writeSSE(w, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": models.StreamDelta{StopReason: stopReason},
		"usage": models.AnthropicUsage{OutputTokens: outputTokens},
	})
	flusher.Flush()

	// Emit message_stop
	writeSSE(w, "message_stop", map[string]interface{}{
		"type": "message_stop",
	})
	flusher.Flush()
}

func writeSSE(w io.Writer, event string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("stream translator: marshal error: %v", err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(jsonData))
}
