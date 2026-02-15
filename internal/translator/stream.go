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
			ID:       "msg_local",
			Type:     "message",
			Role:     "assistant",
			Model:    ollamaModel,
			Content:  []models.ContentBlock{},
			Usage:    models.AnthropicUsage{},
		},
	})
	flusher.Flush()

	// Emit content_block_start
	writeSSE(w, "content_block_start", map[string]interface{}{
		"type":          "content_block_start",
		"index":         0,
		"content_block": models.ContentBlock{Type: "text", Text: ""},
	})
	flusher.Flush()

	// Read Ollama SSE stream
	scanner := bufio.NewScanner(ollamaBody)
	var outputTokens int

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
		text := choice.Delta.Content

		if text != "" {
			outputTokens++
			writeSSE(w, "content_block_delta", map[string]interface{}{
				"type":  "content_block_delta",
				"index": 0,
				"delta": models.StreamDelta{Type: "text_delta", Text: text},
			})
			flusher.Flush()
		}

		if choice.FinishReason != nil {
			break
		}
	}

	// Emit content_block_stop
	writeSSE(w, "content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": 0,
	})
	flusher.Flush()

	// Emit message_delta with stop reason
	writeSSE(w, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": models.StreamDelta{StopReason: "end_turn"},
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
