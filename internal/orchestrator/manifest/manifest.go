// Package manifest loads and represents the local-model registry and the
// permissions allowlist intern reads from disk. Both are user-editable YAML
// files under ~/.intern/.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PlannerTemplateVersion is mixed into the manifest hash so any edit to the
// planner system-prompt template forces a fresh planner block (and one cache
// miss per active session at next planning request). Bump on any
// behavior-affecting change to the template.
const PlannerTemplateVersion = "planner@v1"

// Model describes one local model's capabilities and runtime parameters.
type Model struct {
	ID               string   `yaml:"id"`
	Backend          string   `yaml:"backend"` // vllm-mlx|ollama|llama-cpp|http
	Endpoint         string   `yaml:"endpoint"`
	Model            string   `yaml:"model"` // backend-specific id (e.g. HF repo or ollama tag)
	Capabilities     []string `yaml:"capabilities"`
	AntiCapabilities []string `yaml:"anti_capabilities"`
	MaxInputTokens   int      `yaml:"max_input_tokens"`
	MaxOutputTokens  int      `yaml:"max_output_tokens"`
	AvgLatencyMS     int      `yaml:"avg_latency_ms"`
	CostPer1MTokens  float64  `yaml:"cost_per_1m_tokens_usd"`
	Description      string   `yaml:"description"`
	SupportsTools    bool     `yaml:"supports_tools"`

	// ExtraRequestParams is a free-form bag merged into outgoing chat
	// completion requests. Used for vendor-specific knobs the OpenAI API
	// shape doesn't standardize — e.g. Qwen3's `chat_template_kwargs:
	// {enable_thinking: false}` to suppress reasoning blocks. Affects the
	// manifest hash so changes invalidate cached planner blocks.
	ExtraRequestParams map[string]any `yaml:"extra_request_params,omitempty"`
}

// Manifest is the parsed YAML registry plus its content hash.
type Manifest struct {
	Models []Model `yaml:"models"`

	// Hash is computed from the canonicalized YAML content + the planner
	// template version. The orchestrator pins this onto every plan it
	// generates so an in-flight plan continues against its frozen view of
	// the registry even if the file is edited mid-session.
	Hash string `yaml:"-"`

	// Path is the source path. Empty when constructed in-memory.
	Path string `yaml:"-"`
}

// LoadManifest reads the YAML file at path and computes its content hash.
// A missing file is not an error — the returned manifest has zero models and
// a stable hash derived from the planner template version alone, so the
// orchestrator can still run with no local models configured.
func LoadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	m := &Manifest{Path: path}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, m); err != nil {
			return nil, fmt.Errorf("manifest %s: %w", path, err)
		}
	}
	m.Hash = computeHash(m, raw)
	return m, nil
}

// FindModel returns the Model with the given id, or nil if not present.
func (m *Manifest) FindModel(id string) *Model {
	for i := range m.Models {
		if m.Models[i].ID == id {
			return &m.Models[i]
		}
	}
	return nil
}

// ModelIDs returns the list of all configured model IDs in declared order.
func (m *Manifest) ModelIDs() []string {
	ids := make([]string, len(m.Models))
	for i, mod := range m.Models {
		ids[i] = mod.ID
	}
	return ids
}

// computeHash derives a content hash that intern uses as `manifest:` in
// generated plans. Stable across YAML reformats because we hash the parsed
// canonical form, not the raw bytes.
func computeHash(m *Manifest, raw []byte) string {
	h := sha256.New()
	h.Write([]byte(PlannerTemplateVersion))

	// Sort models by ID so YAML reordering doesn't change the hash.
	models := make([]Model, len(m.Models))
	copy(models, m.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	for _, mod := range models {
		fmt.Fprintf(h, "%s|%s|%s|%s|%v|%v|%d|%d|%d|%v|%t|%v\n",
			mod.ID, mod.Backend, mod.Endpoint, mod.Model,
			mod.Capabilities, mod.AntiCapabilities,
			mod.MaxInputTokens, mod.MaxOutputTokens, mod.AvgLatencyMS,
			mod.CostPer1MTokens, mod.SupportsTools,
			canonicalMap(mod.ExtraRequestParams))
	}

	sum := h.Sum(nil)
	return "0x" + hex.EncodeToString(sum[:8]) // 64-bit hex matches the grammar's hex literal
}

// canonicalMap renders a possibly-nested map[string]any in key-sorted JSON so
// the manifest hash is stable regardless of YAML key ordering. Nil maps
// canonicalize to "null" so empty and absent are equal.
func canonicalMap(m map[string]any) string {
	if m == nil {
		return "null"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:", k)
		switch v := m[k].(type) {
		case map[string]any:
			b.WriteString(canonicalMap(v))
		case map[any]any:
			// yaml.v3 unmarshals nested maps as map[any]any when key types are
			// not strict strings; convert and recurse so the hash still
			// stabilizes across YAML reformats.
			tmp := make(map[string]any, len(v))
			for kk, vv := range v {
				tmp[fmt.Sprint(kk)] = vv
			}
			b.WriteString(canonicalMap(tmp))
		default:
			fmt.Fprintf(&b, "%v", v)
		}
	}
	b.WriteByte('}')
	return b.String()
}
