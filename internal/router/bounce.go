package router

import (
	"strings"

	"github.com/abhishekjha17/intern/internal/models"
)

type RouteDecision string

const (
	RouteLocal RouteDecision = "LOCAL"
	RouteCloud RouteDecision = "CLOUD"
)

// Decide analyzes the prompt for skills like 'web_fetch' or 'boilerplate'.
func Decide(req models.AnthropicRequest) RouteDecision {
	// 1. Get the last user message
	var lastPrompt string
	if len(req.Messages) > 0 {
		lastPrompt = req.Messages[len(req.Messages)-1].Content
	}

	// 2. Skill Detection (Local vs Cloud)
	// In your final POC, this will be a call to llama3.2:1b

	// Example: Routine coding or simple web requests go LOCAL
	lowComplexitySkills := []string{"fetch", "http", "summarize", "boilerplate", "regex"}
	for _, skill := range lowComplexitySkills {
		if strings.Contains(strings.ToLower(lastPrompt), skill) {
			return RouteLocal
		}
	}

	return RouteCloud
}
