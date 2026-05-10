package planner

import (
	"encoding/json"
	"errors"
	"fmt"
)

// EmitPlanToolName is the canonical name of the tool the planner is
// forced to call. Stable across versions; do not rename without a
// manifest hash bump.
const EmitPlanToolName = "emit_plan"

// EmitPlanTool returns the Anthropic tool definition for emit_plan.
// Callers attach this to outgoing planning requests alongside a
// tool_choice forcing the call. The definition is a json.RawMessage so
// callers can inline it into the request body without re-marshaling.
func EmitPlanTool() json.RawMessage {
	const def = `{
  "name": "emit_plan",
  "description": "Emit the orchestration plan as a single program in plan@v1 grammar. The program will be parsed and validated by intern's orchestrator. On parse or semantic-check failure, you will be given a compiler-style error and exactly one retry.",
  "input_schema": {
    "type": "object",
    "required": ["program"],
    "properties": {
      "program": {
        "type": "string",
        "description": "A complete plan@v1 program. Must start with 'plan @v1 {' and end with '}'. See the planner system prompt for grammar reference and worked examples."
      },
      "rationale": {
        "type": "string",
        "description": "One-paragraph explanation of the plan's strategy. Not parsed or executed; recorded in trace logs for offline analysis.",
        "maxLength": 1000
      }
    }
  }
}`
	return json.RawMessage(def)
}

// EmitPlanToolChoice returns the Anthropic tool_choice JSON forcing the
// model to invoke emit_plan rather than emit a free-form reply.
func EmitPlanToolChoice() json.RawMessage {
	return json.RawMessage(`{"type": "tool", "name": "emit_plan"}`)
}

// EmitPlanInput is the deserialized shape of an emit_plan tool_use's input.
type EmitPlanInput struct {
	Program   string `json:"program"`
	Rationale string `json:"rationale,omitempty"`
}

// ExtractFromContent walks an Anthropic response's `content` array,
// returns the first emit_plan tool_use block's input. Used by callers
// that have already deserialized the response body.
//
// Returns ErrNoEmitPlan if no tool_use named emit_plan is present.
func ExtractFromContent(content []json.RawMessage) (*EmitPlanInput, error) {
	for _, raw := range content {
		var hdr struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &hdr); err != nil {
			continue
		}
		if hdr.Type != "tool_use" || hdr.Name != EmitPlanToolName {
			continue
		}
		var in EmitPlanInput
		if err := json.Unmarshal(hdr.Input, &in); err != nil {
			return nil, fmt.Errorf("emit_plan: input not deserializable: %w", err)
		}
		if in.Program == "" {
			return nil, errors.New("emit_plan: empty program")
		}
		return &in, nil
	}
	return nil, ErrNoEmitPlan
}

// ExtractFromMessageJSON parses a full Anthropic /v1/messages JSON
// response body and returns the first emit_plan tool_use input. The
// response shape mirrors:
//
//	{"role":"assistant","content":[{"type":"tool_use","name":"emit_plan","input":{...}}, ...], ...}
func ExtractFromMessageJSON(body []byte) (*EmitPlanInput, error) {
	var resp struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("emit_plan: response body not JSON: %w", err)
	}
	return ExtractFromContent(resp.Content)
}

// ErrNoEmitPlan is returned when the response does not contain an
// emit_plan tool_use block. Callers may treat this as a hard error
// (the planner did not comply) or as a signal to retry.
var ErrNoEmitPlan = errors.New("emit_plan: no emit_plan tool_use in response")
