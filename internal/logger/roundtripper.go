package logger

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Trace is the schema written as a single JSON line to the trace file.
// New pricing-relevant fields are additive and tagged with omitempty so
// older trace files still parse cleanly.
type Trace struct {
	Timestamp       time.Time `json:"timestamp"`
	SessionID       string    `json:"session_id"`
	Model           string    `json:"model"`
	Request         string    `json:"request"`
	Response        string    `json:"response"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	ThinkingTokens  int       `json:"thinking_tokens"`
	CacheReadTokens int       `json:"cache_read_tokens"`

	// CacheCreation{5m,1h}Tokens split the `cache_creation` usage breakdown
	// Anthropic returns when explicit cache TTLs are requested. When the API
	// returns only a top-level `cache_creation_input_tokens` sum, we treat it
	// as 5-minute writes (Anthropic's default cache TTL).
	CacheCreation5mTokens int `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens,omitempty"`

	// Server-side tool usage counts for features that carry separate surcharges
	// beyond token pricing.
	WebSearchRequests     int `json:"web_search_requests,omitempty"`
	CodeExecutionRequests int `json:"code_execution_requests,omitempty"`

	// InferenceGeo mirrors the request's inference_geo field. "us-only" maps
	// to the 1.1x data-residency multiplier; any other value (including empty)
	// uses standard pricing.
	InferenceGeo string `json:"inference_geo,omitempty"`
}

// logEntry is the value sent over the buffered channel to the background worker.
type logEntry struct {
	timestamp    time.Time
	sessionID    string
	model        string
	request      string
	response     string
	contentType  string
	inferenceGeo string
}

// AnthropicUsage mirrors the usage object returned by the Anthropic API.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// ThinkingTokens is populated when the API explicitly breaks out thinking
	// token usage (possible in future API versions). Currently, thinking tokens
	// are bundled into OutputTokens by Anthropic, so this field will typically
	// be zero and ThinkingTokensFromContent is used instead.
	ThinkingTokens           int                 `json:"thinking_tokens"`
	CacheReadInputTokens     int                 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int                 `json:"cache_creation_input_tokens"`
	CacheCreation            *CacheCreationBreak `json:"cache_creation,omitempty"`
	ServerToolUse            *ServerToolUseUsage `json:"server_tool_use,omitempty"`
}

// CacheCreationBreak is the per-TTL breakdown Anthropic returns inside the
// usage object when explicit cache control tiers are used.
type CacheCreationBreak struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

// ServerToolUseUsage counts server-side tool invocations that Anthropic meters
// separately from token usage (web search, code execution, etc.).
type ServerToolUseUsage struct {
	WebSearchRequests     int `json:"web_search_requests"`
	CodeExecutionRequests int `json:"code_execution_requests"`
}

// cacheCreationTokens returns the (5m, 1h) token counts from usage, preferring
// the explicit `cache_creation` sub-object and falling back to attributing the
// top-level `cache_creation_input_tokens` sum to the 5-minute bucket.
func cacheCreationTokens(u AnthropicUsage) (fiveMin, oneHour int) {
	if u.CacheCreation != nil {
		return u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.Ephemeral1hInputTokens
	}
	return u.CacheCreationInputTokens, 0
}

// AnthropicContentBlock represents a single block in the response content array.
type AnthropicContentBlock struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`     // tool name for tool_use/server_tool_use blocks
	Thinking string `json:"thinking,omitempty"` // non-empty for type=="thinking" blocks
}

// AnthropicResponse is the top-level response structure from the Anthropic API.
type AnthropicResponse struct {
	Content []AnthropicContentBlock `json:"content"`
	Usage   AnthropicUsage          `json:"usage"`
}

// ThinkingTokensFromContent estimates the number of thinking tokens by summing
// the character lengths of all type="thinking" content blocks and dividing by 4
// (the standard approximation of 1 token ≈ 4 characters). It is only used when
// the usage object does not provide an explicit thinking token count.
func ThinkingTokensFromContent(blocks []AnthropicContentBlock) int {
	var chars int
	for _, b := range blocks {
		if b.Type == "thinking" {
			chars += len(b.Thinking)
		}
	}
	return chars / 4
}

// SSEMessageStart is the shape of the data payload for "message_start" SSE events.
type SSEMessageStart struct {
	Type    string            `json:"type"`
	Message AnthropicResponse `json:"message"`
}

// SSEMessageDelta is the shape of the data payload for "message_delta" SSE events.
type SSEMessageDelta struct {
	Type  string         `json:"type"`
	Usage AnthropicUsage `json:"usage"`
}

// SSEContentBlockStart is the shape of the data payload for "content_block_start" SSE events.
type SSEContentBlockStart struct {
	ContentBlock AnthropicContentBlock `json:"content_block"`
}

// SSEContentBlockDelta is the shape of the data payload for "content_block_delta" SSE events.
type SSEContentBlockDelta struct {
	Delta struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"` // populated for thinking_delta
	} `json:"delta"`
}

// UsageFromSSE parses an SSE response body and extracts aggregated usage info
// by scanning message_start, content_block_start, content_block_delta, and
// message_delta events. Thinking text is accumulated from thinking_delta events
// since message_start always carries an empty content array in streaming responses.
//
// Usage fields are merged across events: message_start carries input/cache
// totals; message_delta carries final output_tokens and — when server-side
// tools are used — server_tool_use counts.
func UsageFromSSE(body string) (usage AnthropicUsage, content []AnthropicContentBlock) {
	var thinkingBuf strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		// Try message_start — carries initial usage (input, cache, per-TTL breakdown).
		var ms SSEMessageStart
		if json.Unmarshal([]byte(data), &ms) == nil && ms.Type == "message_start" {
			u := ms.Message.Usage
			usage.InputTokens = u.InputTokens
			usage.CacheReadInputTokens = u.CacheReadInputTokens
			usage.CacheCreationInputTokens = u.CacheCreationInputTokens
			if u.CacheCreation != nil {
				usage.CacheCreation = u.CacheCreation
			}
			if u.ServerToolUse != nil {
				usage.ServerToolUse = u.ServerToolUse
			}
		}

		// Try content_block_delta — accumulate thinking text from thinking_delta events.
		var cbd SSEContentBlockDelta
		if json.Unmarshal([]byte(data), &cbd) == nil && cbd.Delta.Type == "thinking_delta" {
			thinkingBuf.WriteString(cbd.Delta.Thinking)
		}

		// Try message_delta — carries final output token count and may update
		// server_tool_use counts when tools are invoked mid-stream.
		var md SSEMessageDelta
		if json.Unmarshal([]byte(data), &md) == nil && md.Type == "message_delta" {
			usage.OutputTokens = md.Usage.OutputTokens
			if md.Usage.ServerToolUse != nil {
				usage.ServerToolUse = md.Usage.ServerToolUse
			}
			if md.Usage.CacheCreation != nil && usage.CacheCreation == nil {
				usage.CacheCreation = md.Usage.CacheCreation
			}
		}
	}

	// Build content blocks from accumulated thinking text so
	// ThinkingTokensFromContent can estimate the thinking token count.
	if thinkingBuf.Len() > 0 {
		content = append(content, AnthropicContentBlock{
			Type:     "thinking",
			Thinking: thinkingBuf.String(),
		})
	}

	return usage, content
}

// anthropicRequest captures only the fields we need from the outgoing request body.
type anthropicRequest struct {
	Model        string            `json:"model"`
	Messages     []json.RawMessage `json:"messages"`
	InferenceGeo string            `json:"inference_geo,omitempty"`
}

// sessionID derives a stable 16-char hex session identifier by hashing the raw
// JSON of the first message in the conversation. All turns of the same Claude
// Code session share an identical first message, so they hash to the same ID.
// Returns an empty string when the messages array is absent or empty.
func sessionID(messages []json.RawMessage) string {
	if len(messages) == 0 {
		return ""
	}
	sum := sha256.Sum256(messages[0])
	return hex.EncodeToString(sum[:])[:16]
}

// LoggingRoundTripper implements http.RoundTripper. It transparently forwards
// every request to the Anthropic API while capturing the request/response bodies
// and shipping them to a background worker for async JSONL logging.
type LoggingRoundTripper struct {
	inner   http.RoundTripper
	ch      chan logEntry
	done    chan struct{}
	logFile string
}

// New creates a LoggingRoundTripper and starts its background worker.
// logFile is the path to the JSONL trace file (default: "intern_traces.jsonl").
// bufSize is the channel buffer depth (default: 64).
// Call Close() to flush pending entries and stop the worker.
func New(logFile string, bufSize int) *LoggingRoundTripper {
	if logFile == "" {
		logFile = "intern_traces.jsonl"
	}
	if bufSize <= 0 {
		bufSize = 64
	}
	l := &LoggingRoundTripper{
		inner:   http.DefaultTransport,
		ch:      make(chan logEntry, bufSize),
		done:    make(chan struct{}),
		logFile: logFile,
	}
	go l.worker()
	return l
}

// teeReadCloser wraps a response body so that every byte the caller reads is
// also captured in a buffer. When Close is called the complete response is
// enqueued as a trace entry on the logging channel.
type teeReadCloser struct {
	orig io.ReadCloser
	buf  bytes.Buffer
	ch   chan logEntry
	meta logEntry // partially filled; response populated on Close
}

func (t *teeReadCloser) Read(p []byte) (int, error) {
	n, err := t.orig.Read(p)
	if n > 0 {
		t.buf.Write(p[:n])
	}
	return n, err
}

func (t *teeReadCloser) Close() error {
	err := t.orig.Close()

	t.meta.response = t.buf.String()

	// Non-blocking send: drop the entry if the buffer is full rather than
	// adding latency to the caller's hot path.
	select {
	case t.ch <- t.meta:
	default:
		log.Println("intern/logger: channel full, dropping trace")
	}

	return err
}

// RoundTrip implements http.RoundTripper.
//
// It snapshots the request body, forwards the request, then wraps the response
// body with a tee so streaming data flows through to the caller in real-time
// while being captured for async trace logging.
func (l *LoggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Snapshot the request body.
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	// Strip Accept-Encoding so the upstream sends uncompressed responses.
	// This lets us parse the response body as plain text for trace logging.
	req.Header.Del("Accept-Encoding")

	ts := time.Now()

	resp, err := l.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	// Extract model, session ID, and inference_geo from the request JSON (best-effort).
	var model, sid, geo string
	var ar anthropicRequest
	if json.Unmarshal(reqBody, &ar) == nil {
		model = ar.Model
		sid = sessionID(ar.Messages)
		geo = ar.InferenceGeo
	}

	// Wrap the response body so we capture it as the caller streams through it.
	resp.Body = &teeReadCloser{
		orig: resp.Body,
		ch:   l.ch,
		meta: logEntry{
			timestamp:    ts,
			sessionID:    sid,
			model:        model,
			request:      string(reqBody),
			contentType:  resp.Header.Get("Content-Type"),
			inferenceGeo: geo,
		},
	}

	return resp, nil
}

// Close signals the background worker to stop and waits for it to finish
// writing all queued entries before returning.
func (l *LoggingRoundTripper) Close() {
	close(l.ch)
	<-l.done
}

// worker drains the log channel, parses usage data from each Anthropic response,
// and appends one JSON line per interaction to the trace file.
func (l *LoggingRoundTripper) worker() {
	defer close(l.done)

	f, err := os.OpenFile(l.logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("intern/logger: cannot open trace file %s: %v", l.logFile, err)
		for range l.ch { // drain so Close() is never blocked
		}
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)

	for entry := range l.ch {
		var usage AnthropicUsage
		var content []AnthropicContentBlock

		isSSE := strings.Contains(entry.contentType, "text/event-stream")
		if isSSE {
			usage, content = UsageFromSSE(entry.response)
		} else {
			var ar AnthropicResponse
			if err := json.Unmarshal([]byte(entry.response), &ar); err != nil {
				log.Printf("intern/logger: failed to parse response JSON: %v", err)
			}
			usage = ar.Usage
			content = ar.Content
		}

		// Prefer the explicit field; fall back to estimating from content blocks.
		thinkingTokens := usage.ThinkingTokens
		if thinkingTokens == 0 {
			thinkingTokens = ThinkingTokensFromContent(content)
		}

		fiveMin, oneHour := cacheCreationTokens(usage)
		var webSearch, codeExec int
		if usage.ServerToolUse != nil {
			webSearch = usage.ServerToolUse.WebSearchRequests
			codeExec = usage.ServerToolUse.CodeExecutionRequests
		}

		trace := Trace{
			Timestamp:             entry.timestamp,
			SessionID:             entry.sessionID,
			Model:                 entry.model,
			Request:               entry.request,
			Response:              entry.response,
			InputTokens:           usage.InputTokens,
			OutputTokens:          usage.OutputTokens,
			ThinkingTokens:        thinkingTokens,
			CacheReadTokens:       usage.CacheReadInputTokens,
			CacheCreation5mTokens: fiveMin,
			CacheCreation1hTokens: oneHour,
			WebSearchRequests:     webSearch,
			CodeExecutionRequests: codeExec,
			InferenceGeo:          entry.inferenceGeo,
		}

		if err := enc.Encode(trace); err != nil {
			log.Printf("intern/logger: failed to write trace: %v", err)
		}
	}
}
