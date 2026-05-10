package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"
)

// emitPlanStub returns a minimal tool definition; the rewriter only
// needs valid JSON, the shape is checked elsewhere.
func emitPlanStub() json.RawMessage {
	return json.RawMessage(`{"name":"emit_plan","description":"stub","input_schema":{"type":"object"}}`)
}

// firstSystemBlockCacheControl returns the cache_control map of the
// first block of the rewritten system field — that's the planner block.
func firstSystemBlockCacheControl(t *testing.T, rewritten []byte) map[string]any {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rewritten, &raw); err != nil {
		t.Fatalf("unmarshal rewritten: %v", err)
	}
	var sys []json.RawMessage
	if err := json.Unmarshal(raw["system"], &sys); err != nil {
		t.Fatalf("system not an array: %v", err)
	}
	if len(sys) == 0 {
		t.Fatal("system empty")
	}
	var firstBlock map[string]any
	if err := json.Unmarshal(sys[0], &firstBlock); err != nil {
		t.Fatalf("first block: %v", err)
	}
	cc, _ := firstBlock["cache_control"].(map[string]any)
	return cc
}

func TestRewriteForPlanning_PlannerCacheControlMatches1hTTL(t *testing.T) {
	// Claude Code attaches `ttl:1h` blocks to its system content. Without
	// alignment, prepending an ephemeral (5m default) block ahead of a
	// 1h block violates Anthropic's "longer TTL first" rule and the
	// upstream returns 400.
	body := []byte(`{
		"model":"claude-opus-4-7",
		"system":[
			{"type":"text","text":"you are claude code","cache_control":{"type":"ephemeral","ttl":"1h"}}
		],
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, err := rewriteForPlanning(body, "PLANNER", emitPlanStub())
	if err != nil {
		t.Fatalf("rewriteForPlanning: %v", err)
	}

	cc := firstSystemBlockCacheControl(t, out)
	if cc["type"] != "ephemeral" {
		t.Errorf("planner cache_control.type = %v, want ephemeral", cc["type"])
	}
	if cc["ttl"] != "1h" {
		t.Errorf("planner cache_control.ttl = %v, want 1h (to match existing block)", cc["ttl"])
	}
}

func TestRewriteForPlanning_PlannerCacheControlDefaults5m(t *testing.T) {
	// No cache_control on existing system → planner block should NOT
	// carry an explicit ttl (Anthropic defaults to 5m). Setting "5m"
	// would still be valid but would change the wire shape and risk
	// missing cache hits on already-deployed callers.
	body := []byte(`{
		"model":"claude-opus-4-7",
		"system":"plain string system",
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, err := rewriteForPlanning(body, "PLANNER", emitPlanStub())
	if err != nil {
		t.Fatalf("rewriteForPlanning: %v", err)
	}

	cc := firstSystemBlockCacheControl(t, out)
	if _, hasTTL := cc["ttl"]; hasTTL {
		t.Errorf("planner cache_control should have no ttl when existing has none, got %v", cc)
	}
}

func TestRewriteForPlanning_NoSystem_NoTTL(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-7",
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, err := rewriteForPlanning(body, "PLANNER", emitPlanStub())
	if err != nil {
		t.Fatalf("rewriteForPlanning: %v", err)
	}

	cc := firstSystemBlockCacheControl(t, out)
	if _, hasTTL := cc["ttl"]; hasTTL {
		t.Errorf("planner cache_control should have no ttl when system is absent, got %v", cc)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("planner cache_control.type = %v, want ephemeral", cc["type"])
	}
}

func TestRewriteForPlanning_PreservesThinkingWithAutoToolChoice(t *testing.T) {
	// Thinking + forced tool_choice is a 400. Thinking + auto tool_choice
	// is allowed and gives the planner chain-of-thought before deciding
	// whether to emit_plan or just answer directly.
	body := []byte(`{
		"model":"claude-opus-4-7",
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	out, err := rewriteForPlanning(body, "PLANNER", emitPlanStub())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["thinking"]; !ok {
		t.Errorf("rewritten body should preserve `thinking`; got: %s", out)
	}
	var tc map[string]any
	if err := json.Unmarshal(raw["tool_choice"], &tc); err != nil {
		t.Fatal(err)
	}
	if tc["type"] != "auto" {
		t.Errorf("tool_choice.type = %q, want auto (forced is incompatible with thinking)", tc["type"])
	}
}

func TestRewriteForPlanning_PreservesExistingBlocks(t *testing.T) {
	// Sanity: existing system blocks are preserved (not replaced) and
	// the planner block sits in front. Pre-existing cache_control on
	// the user block stays untouched.
	body := []byte(`{
		"model":"claude-opus-4-7",
		"system":[
			{"type":"text","text":"USER-BLOCK","cache_control":{"type":"ephemeral","ttl":"1h"}}
		],
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, err := rewriteForPlanning(body, "PLANNER", emitPlanStub())
	if err != nil {
		t.Fatalf("rewriteForPlanning: %v", err)
	}
	if !strings.Contains(string(out), "USER-BLOCK") {
		t.Errorf("rewritten body lost user block: %s", out)
	}
	if !strings.Contains(string(out), "PLANNER") {
		t.Errorf("rewritten body missing planner block: %s", out)
	}
}
