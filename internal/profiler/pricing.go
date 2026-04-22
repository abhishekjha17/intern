package profiler

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// ModelPricing holds per-million-token rates for a single model.
// All rates are USD per 1,000,000 tokens.
type ModelPricing struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

// WebSearchPerThousand is the server-side web-search rate charged on top of
// token usage — flat across models at $10 per 1,000 searches.
const WebSearchPerThousand = 10.0

// DataResidencyUSMultiplier is applied to every token rate when an Anthropic
// Messages API request pins inference to a specific geography.
const DataResidencyUSMultiplier = 1.1

// defaultPricing mirrors the Anthropic public pricing table
// (https://platform.claude.com/docs/en/about-claude/pricing) as of 2026-04.
// Ship updates by editing this map or by dropping a pricing.json file at
// $XDG_CONFIG_HOME/intern/pricing.json.
//
// Models in the same family share rates intentionally — Anthropic has kept
// Opus 4.5/4.6/4.7 on the same $5/$25 tier, and the $15/$75 generation of
// Opus spans 4, 4.0, and 4.1.
var defaultPricing = map[string]ModelPricing{
	// Opus 4.x — $5 input / $25 output family
	"claude-opus-4-7": {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10},
	"claude-opus-4-6": {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10},
	"claude-opus-4-5": {Input: 5, Output: 25, CacheRead: 0.50, CacheWrite5m: 6.25, CacheWrite1h: 10},

	// Opus 4 / 4.0 / 4.1 — $15 input / $75 output family
	"claude-opus-4-1": {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite5m: 18.75, CacheWrite1h: 30},
	"claude-opus-4-0": {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite5m: 18.75, CacheWrite1h: 30},
	"claude-opus-4":   {Input: 15, Output: 75, CacheRead: 1.50, CacheWrite5m: 18.75, CacheWrite1h: 30},

	// Sonnet 4.x
	"claude-sonnet-4-6":          {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6},
	"claude-sonnet-4-5":          {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6},
	"claude-sonnet-4-5-20241022": {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6},
	"claude-sonnet-4-0":          {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6},
	"claude-sonnet-4":            {Input: 3, Output: 15, CacheRead: 0.30, CacheWrite5m: 3.75, CacheWrite1h: 6},

	// Haiku
	"claude-haiku-4-5":          {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2},
	"claude-haiku-4-5-20251001": {Input: 1, Output: 5, CacheRead: 0.10, CacheWrite5m: 1.25, CacheWrite1h: 2},
	"claude-haiku-3-5":          {Input: 0.80, Output: 4, CacheRead: 0.08, CacheWrite5m: 1.00, CacheWrite1h: 1.60},
	"claude-haiku-3":            {Input: 0.25, Output: 1.25, CacheRead: 0.03, CacheWrite5m: 0.30, CacheWrite1h: 0.50},
}

// pricingState holds the currently active pricing table and unknown-model
// warning cache. Guarded by its own mutex so LoadPricing is safe to call
// concurrently with CostForTokens.
var (
	pricingMu     sync.RWMutex
	activePricing = clonePricing(defaultPricing)
	unknownModels = map[string]bool{}
)

// dateSuffixRe matches a trailing `-YYYYMMDD` date stamp that Anthropic attaches
// to versioned model IDs (e.g. `claude-sonnet-4-5-20241022`).
var dateSuffixRe = regexp.MustCompile(`-\d{8}$`)

// Pricing returns a snapshot of the currently active pricing table.
// The returned map is a copy; callers may read it without holding the lock.
func Pricing() map[string]ModelPricing {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	return clonePricing(activePricing)
}

// LoadPricing merges overrides from a JSON file on top of the embedded
// defaults and installs the result as the active pricing table. Each call
// fully replaces the active table, so it safely resets state between runs.
//
// When path is empty, LoadPricing looks at the default location
// ($XDG_CONFIG_HOME/intern/pricing.json, falling back to
// ~/.config/intern/pricing.json) and silently uses embedded defaults if
// the file is absent. When path is non-empty the file must exist.
//
// Returns the source describing where pricing was loaded from:
// "embedded" (no override applied) or the absolute file path.
func LoadPricing(path string) (source string, err error) {
	resolved := path
	if resolved == "" {
		if p, ok := defaultPricingPath(); ok {
			if _, statErr := os.Stat(p); statErr == nil {
				resolved = p
			}
		}
	}

	merged := clonePricing(defaultPricing)
	source = "embedded"

	if resolved != "" {
		data, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return "", fmt.Errorf("read pricing file %s: %w", resolved, readErr)
		}
		var overrides map[string]ModelPricing
		if jsonErr := json.Unmarshal(data, &overrides); jsonErr != nil {
			return "", fmt.Errorf("parse pricing file %s: %w", resolved, jsonErr)
		}
		for model, p := range overrides {
			merged[model] = p
		}
		source = resolved
	}

	pricingMu.Lock()
	activePricing = merged
	unknownModels = map[string]bool{}
	pricingMu.Unlock()

	return source, nil
}

// defaultPricingPath returns the expected location of the optional
// pricing override file. The second return is false when the user has
// no home directory (e.g. a sandbox with no HOME).
func defaultPricingPath() (string, bool) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "intern", "pricing.json"), true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".config", "intern", "pricing.json"), true
}

func clonePricing(src map[string]ModelPricing) map[string]ModelPricing {
	out := make(map[string]ModelPricing, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// LookupPricing returns the pricing for a model ID. It tries an exact match
// first and then strips a trailing `-YYYYMMDD` date suffix, so a versioned
// ID like `claude-opus-4-7-20260301` resolves to the entry for
// `claude-opus-4-7` when the dated variant isn't explicitly listed.
func LookupPricing(model string) (ModelPricing, bool) {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	if p, ok := activePricing[model]; ok {
		return p, true
	}
	if stripped := dateSuffixRe.ReplaceAllString(model, ""); stripped != model {
		if p, ok := activePricing[stripped]; ok {
			return p, true
		}
	}
	return ModelPricing{}, false
}

// CostForTokens calculates the dollar cost for a model and token counts.
// cacheWrite5m and cacheWrite1h are billed separately because Anthropic
// charges 1.25x and 2x base input respectively; collapsing them loses
// ~60% of the cost for a 1-hour write.
//
// Returns 0 and logs a one-time warning per unrecognized model.
func CostForTokens(model string, input, output, cacheRead, cacheWrite5m, cacheWrite1h int) float64 {
	p, ok := LookupPricing(model)
	if !ok {
		warnUnknownModel(model)
		return 0
	}
	cost := float64(input) / 1_000_000 * p.Input
	cost += float64(output) / 1_000_000 * p.Output
	cost += float64(cacheRead) / 1_000_000 * p.CacheRead
	cost += float64(cacheWrite5m) / 1_000_000 * p.CacheWrite5m
	cost += float64(cacheWrite1h) / 1_000_000 * p.CacheWrite1h
	return cost
}

// CostBreakdown is the per-dimension dollar cost for a single API call, with
// the data-residency multiplier and the web-search surcharge already applied.
// The sum of every non-Total field equals Total.
type CostBreakdown struct {
	Input               float64
	Output              float64
	CacheRead           float64
	CacheWrite5m        float64
	CacheWrite1h        float64
	WebSearch           float64
	DataResidencyAdjust float64 // additional cost from the 1.1x multiplier (already included in Input/Output/Cache* above)
	Total               float64
}

// ComputeCost derives every billed component for one Messages API call.
// The returned Input/Output/Cache* fields include the data-residency
// multiplier so their sum (plus WebSearch) equals Total.
// DataResidencyAdjust is reported separately for accounting/display.
func ComputeCost(model string, input, output, cacheRead, cacheWrite5m, cacheWrite1h, webSearchRequests int, inferenceGeo string) CostBreakdown {
	webCost := WebSearchCost(webSearchRequests)

	p, ok := LookupPricing(model)
	if !ok {
		warnUnknownModel(model)
		return CostBreakdown{WebSearch: webCost, Total: webCost}
	}

	mult := DataResidencyMultiplier(inferenceGeo)
	b := CostBreakdown{
		Input:        float64(input) / 1_000_000 * p.Input * mult,
		Output:       float64(output) / 1_000_000 * p.Output * mult,
		CacheRead:    float64(cacheRead) / 1_000_000 * p.CacheRead * mult,
		CacheWrite5m: float64(cacheWrite5m) / 1_000_000 * p.CacheWrite5m * mult,
		CacheWrite1h: float64(cacheWrite1h) / 1_000_000 * p.CacheWrite1h * mult,
		WebSearch:    webCost,
	}
	tokenTotal := b.Input + b.Output + b.CacheRead + b.CacheWrite5m + b.CacheWrite1h
	if mult > 1.0 {
		// The adjust is the share of tokenTotal contributed by the multiplier:
		// tokenTotal = base * mult, so base = tokenTotal / mult, adjust = tokenTotal - base.
		b.DataResidencyAdjust = tokenTotal - tokenTotal/mult
	}
	b.Total = tokenTotal + b.WebSearch
	return b
}

// WebSearchCost returns the surcharge for server-side web_search_20260209 tool
// usage at the flat $10 / 1,000 requests rate.
func WebSearchCost(requests int) float64 {
	return float64(requests) / 1000.0 * WebSearchPerThousand
}

// DataResidencyMultiplier returns the multiplier applied to token costs based
// on the `inference_geo` request field. Currently only "us-only" carries a
// premium (1.1x); global routing uses standard pricing.
func DataResidencyMultiplier(inferenceGeo string) float64 {
	if inferenceGeo == "us-only" {
		return DataResidencyUSMultiplier
	}
	return 1.0
}

func warnUnknownModel(model string) {
	pricingMu.Lock()
	warned := unknownModels[model]
	unknownModels[model] = true
	pricingMu.Unlock()
	if warned {
		return
	}
	log.Printf("warning: unknown model %q — cost will be $0.00 (add it to your pricing.json or to defaultPricing)", model)
}
