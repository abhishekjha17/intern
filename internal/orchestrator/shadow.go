package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ShadowHook is a thin observer the proxy fires on each outgoing request
// when --orchestrate=shadow is set. It does NOT make any external API
// calls — its job is to render the planner block once per session and
// log it to disk so the operator can verify the shape, size, and
// stability of what would be sent in full-orchestrate mode.
//
// The hook is pure observation: a request that triggers the hook is
// still forwarded to Anthropic unchanged.
type ShadowHook struct {
	o      *Orchestrator
	logger *shadowLogger

	mu          sync.Mutex
	loggedSess  map[string]bool // session_id → already-logged
	plannerOnce sync.Once
}

// NewShadowHook returns a hook that writes shadow log entries to
// `<configDir>/shadow.jsonl`. Pass an empty path to use the default
// `~/.intern/shadow.jsonl`.
func NewShadowHook(o *Orchestrator, logPath string) (*ShadowHook, error) {
	if logPath == "" {
		logPath = filepath.Join(DefaultConfigDir(), "shadow.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("shadow: create log dir: %w", err)
	}
	sl, err := newShadowLogger(logPath)
	if err != nil {
		return nil, err
	}
	return &ShadowHook{
		o:          o,
		logger:     sl,
		loggedSess: map[string]bool{},
	}, nil
}

// OnRequest is called by the proxy with the parsed session id and raw
// request body bytes. It is non-blocking: even if disk writes take
// time, OnRequest returns quickly because the underlying logger writes
// asynchronously.
//
// Only the FIRST request per session triggers the hook; subsequent
// requests in the same session are skipped (the planner block is
// session-stable, so logging it on every request would be pure noise).
func (h *ShadowHook) OnRequest(sessionID string, model string, reqBody []byte) {
	if h == nil || sessionID == "" {
		return
	}
	h.mu.Lock()
	already := h.loggedSess[sessionID]
	if !already {
		h.loggedSess[sessionID] = true
	}
	h.mu.Unlock()
	if already {
		return
	}

	block := h.o.PlannerBlock()
	tool := h.o.EmitPlanTool()

	entry := shadowEntry{
		Timestamp:        time.Now().UTC(),
		SessionID:        sessionID,
		Model:            model,
		ManifestHash:     h.o.Manifest.Hash,
		PlannerBytes:     len(block),
		PlannerSHA256:    sha256short(block),
		EmitPlanToolName: "emit_plan",
		EmitPlanToolJSON: string(tool),
	}
	h.logger.Write(entry)
}

// Close flushes pending shadow log writes.
func (h *ShadowHook) Close() error {
	if h == nil {
		return nil
	}
	return h.logger.Close()
}

// shadowEntry is the JSONL record written per first-of-session request.
// Only metadata is logged (sizes, hashes, model) — never the user prompt
// or the rendered block contents, to keep the log compact and to avoid
// duplicating sensitive material that's already in the trace file.
type shadowEntry struct {
	Timestamp        time.Time `json:"timestamp"`
	SessionID        string    `json:"session_id"`
	Model            string    `json:"model"`
	ManifestHash     string    `json:"manifest_hash"`
	PlannerBytes     int       `json:"planner_bytes"`
	PlannerSHA256    string    `json:"planner_sha256_8"` // 8-byte hex prefix
	EmitPlanToolName string    `json:"emit_plan_tool_name"`
	EmitPlanToolJSON string    `json:"emit_plan_tool_json"`
}

func sha256short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "0x" + hex.EncodeToString(sum[:8])
}

// shadowLogger is a tiny async writer; one goroutine drains a channel
// and appends JSON-encoded lines to the file. The buffer is small —
// shadow logging is a slow-path observation tool, not a hot trace.
type shadowLogger struct {
	ch   chan shadowEntry
	done chan struct{}
}

func newShadowLogger(path string) (*shadowLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("shadow: open log: %w", err)
	}
	sl := &shadowLogger{
		ch:   make(chan shadowEntry, 16),
		done: make(chan struct{}),
	}
	go func() {
		defer close(sl.done)
		defer f.Close()
		enc := json.NewEncoder(f)
		for e := range sl.ch {
			if err := enc.Encode(e); err != nil {
				log.Printf("intern/shadow: encode failed: %v", err)
			}
		}
	}()
	return sl, nil
}

func (s *shadowLogger) Write(e shadowEntry) {
	select {
	case s.ch <- e:
	default:
		log.Printf("intern/shadow: log buffer full, dropping entry for session %s", e.SessionID)
	}
}

func (s *shadowLogger) Close() error {
	close(s.ch)
	<-s.done
	return nil
}
