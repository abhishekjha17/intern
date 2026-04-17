package profiler

import "strings"

var explorationTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true,
	"Agent": true, "WebSearch": true, "WebFetch": true,
}

var executionTools = map[string]bool{
	"Edit": true, "Write": true, "NotebookEdit": true, "Skill": true,
}

var planningTools = map[string]bool{
	"EnterPlanMode": true, "ExitPlanMode": true, "AskUserQuestion": true,
	"TaskCreate": true, "TaskUpdate": true, "TaskList": true, "TaskGet": true,
}

// classifyBashCommand categorizes a Bash command string into a phase.
func classifyBashCommand(cmd string) string {
	lower := strings.ToLower(cmd)

	// Verification patterns
	for _, pat := range []string{"test", "check", "verify", "build", "lint"} {
		if strings.Contains(lower, pat) {
			return "verification"
		}
	}
	// Exploration patterns
	for _, pat := range []string{"ls ", "wc ", "find ", "head ", "tail ", "cat "} {
		if strings.Contains(lower, pat) {
			return "exploration"
		}
	}
	for _, pat := range []string{"git status", "git log", "git diff", "git show", "git branch"} {
		if strings.Contains(lower, pat) {
			return "exploration"
		}
	}
	// Execution git commands
	for _, pat := range []string{"git push", "git commit", "git add", "git rebase"} {
		if strings.Contains(lower, pat) {
			return "execution"
		}
	}
	return "execution"
}

// ClassifyPhase determines the conversation phase based on tools called and
// Bash command content.
func ClassifyPhase(toolsCalled []string, bashCommands []string) string {
	if len(toolsCalled) == 0 {
		return "conversation"
	}

	counts := map[string]int{
		"exploration":  0,
		"execution":    0,
		"verification": 0,
		"planning":     0,
	}

	bashIdx := 0
	for _, tool := range toolsCalled {
		switch {
		case explorationTools[tool]:
			counts["exploration"]++
		case executionTools[tool]:
			counts["execution"]++
		case planningTools[tool]:
			counts["planning"]++
		case tool == "Bash":
			phase := "execution"
			if bashIdx < len(bashCommands) {
				phase = classifyBashCommand(bashCommands[bashIdx])
				bashIdx++
			}
			counts[phase]++
		default:
			counts["execution"]++
		}
	}

	// Pick highest count. Ties: execution > verification > exploration > planning.
	priority := []string{"execution", "verification", "exploration", "planning"}
	best := priority[0]
	for _, p := range priority[1:] {
		if counts[p] > counts[best] {
			best = p
		}
	}
	return best
}

// ClassifyComplexity assigns a complexity level using a score-based heuristic.
func ClassifyComplexity(msg *MessageProfile) string {
	if msg.MaxTokens == 1 {
		return "trivial"
	}

	score := 0

	// Output volume
	switch {
	case msg.OutputTokens < 50:
		// +0
	case msg.OutputTokens < 300:
		score++
	case msg.OutputTokens < 1000:
		score += 2
	default:
		score += 3
	}

	// Tool count
	switch {
	case len(msg.ToolsCalled) == 0:
		// +0
	case len(msg.ToolsCalled) <= 2:
		score++
	default:
		score += 2
	}

	// Tool diversity
	unique := map[string]bool{}
	for _, t := range msg.ToolsCalled {
		unique[t] = true
	}
	if len(unique) >= 3 {
		score += 2
	}

	// Thinking
	if msg.HasThinking {
		score += 2
	}

	// Mixed output
	if msg.OutputType == "mixed" {
		score++
	}

	switch {
	case score <= 1:
		return "trivial"
	case score <= 3:
		return "mechanical"
	case score <= 6:
		return "reasoning"
	default:
		return "creative"
	}
}

// ClassifyDependency determines whether the message is independent, a tool
// continuation, or a conversational continuation.
func ClassifyDependency(request string) string {
	msgCount := extractMessageCount(request)
	if msgCount <= 2 {
		return "independent"
	}
	if extractHasToolResult(request) {
		return "tool_continuation"
	}
	return "conversation_continuation"
}

// ClassifyOffload determines if a message could be offloaded to a local model.
func ClassifyOffload(msg *MessageProfile) (bool, string) {
	if msg.MaxTokens == 1 {
		return true, "health_check"
	}
	if msg.Dependency == "tool_continuation" && msg.OutputTokens < 500 {
		return true, "tool_result_continuation"
	}
	if msg.Complexity == "trivial" {
		return true, "trivial_task"
	}
	return false, ""
}
