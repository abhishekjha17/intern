package profiler

import "log"

// ModelPricing holds per-million-token rates for a model.
type ModelPricing struct {
	Input       float64 // base input tokens
	Output      float64 // output tokens
	CacheRead   float64 // cache hit / read tokens
	CacheCreate float64 // cache write tokens (5-min default)
}

// Pricing maps model IDs to their per-million-token pricing.
// Source: https://platform.claude.com/docs/en/about-claude/pricing
var Pricing = map[string]ModelPricing{
	"claude-opus-4-6":           {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheCreate: 6.25},
	"claude-sonnet-4-6":         {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheCreate: 3.75},
	"claude-sonnet-4-5-20241022": {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheCreate: 3.75},
	"claude-haiku-4-5-20251001": {Input: 1.00, Output: 5.00, CacheRead: 0.10, CacheCreate: 1.25},
}

// unknownModels tracks models we've already warned about to avoid log spam.
var unknownModels = map[string]bool{}

// CostForTokens calculates the dollar cost for a given model and token counts.
// Returns 0 and logs a warning for unrecognized models.
func CostForTokens(model string, input, output, cacheRead, cacheCreation int) float64 {
	p, ok := Pricing[model]
	if !ok {
		if !unknownModels[model] {
			unknownModels[model] = true
			log.Printf("warning: unknown model %q — cost will be $0.00 (add to pricing.go)", model)
		}
		return 0
	}
	cost := float64(input) / 1_000_000 * p.Input
	cost += float64(output) / 1_000_000 * p.Output
	cost += float64(cacheRead) / 1_000_000 * p.CacheRead
	cost += float64(cacheCreation) / 1_000_000 * p.CacheCreate
	return cost
}
