package profiler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/abhishekjha17/intern/internal/logger"
)

// LoadTraces reads JSONL trace files and returns all traces with a non-empty model.
// Malformed lines are skipped with a warning.
func LoadTraces(files []string) ([]logger.Trace, error) {
	var traces []logger.Trace
	for _, path := range files {
		ts, err := loadFile(path)
		if err != nil {
			return nil, err
		}
		traces = append(traces, ts...)
	}
	return traces, nil
}

func loadFile(path string) ([]logger.Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var traces []logger.Trace
	var skipped int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max line
	for scanner.Scan() {
		var t logger.Trace
		if err := json.Unmarshal(scanner.Bytes(), &t); err != nil {
			skipped++
			continue
		}
		if t.Model == "" {
			continue
		}
		traces = append(traces, t)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	if skipped > 0 {
		log.Printf("warning: skipped %d malformed lines in %s", skipped, path)
	}
	return traces, nil
}

// Analyze processes traces and produces a full ProfileReport.
func Analyze(traces []logger.Trace, files []string) *ProfileReport {
	report := &ProfileReport{Files: files}

	// Accumulators
	modelCosts := map[string]*ModelCost{}
	modelTokens := map[string]*ModelTokenStats{}
	toolCounts := map[string]int{}
	blockCounts := map[string]int{}
	sessionMap := map[string]*sessionAccum{}
	phaseCounts := map[string]int{}
	complexityCounts := map[string]int{}
	offloadReasons := map[string]int{}
	var totalCost float64
	var offloadSavings float64

	// Context & memory accumulators
	sessionLastMsgCount := map[string]int{}
	var totalMsgCount, maxMsgCount int
	var totalSystemPromptSize int
	var compactionEvents int
	var contextGrowthSteps, contextGrowthTotal int
	var totalMemoryFilesLoaded, maxMemoryFilesLoaded int
	memoryFileAccess := map[string]*FileAccessCount{}
	var totalMemoryOps int

	thinking := ThinkingAnalysis{TotalMessages: len(traces)}

	for i, t := range traces {
		// Single-pass extraction from response
		rf := ExtractResponse(t.Response)
		toolsCalled := extractToolsCalled(rf.Blocks)
		maxTokens := extractMaxTokens(t.Request)

		cost := CostForTokens(t.Model, t.InputTokens, t.OutputTokens, t.CacheReadTokens, rf.CacheCreation)

		// Build per-message profile
		blockTypes := make([]string, len(rf.Blocks))
		for j, b := range rf.Blocks {
			blockTypes[j] = b.Type
		}

		msg := MessageProfile{
			Index:               i,
			Timestamp:           t.Timestamp,
			SessionID:           t.SessionID,
			Model:               t.Model,
			InputTokens:         t.InputTokens,
			OutputTokens:        t.OutputTokens,
			CacheReadTokens:     t.CacheReadTokens,
			CacheCreationTokens: rf.CacheCreation,
			ThinkingTokens:      t.ThinkingTokens,
			Cost:                cost,
			OutputType:          classifyOutputType(rf.Blocks),
			ToolsCalled:         toolsCalled,
			ContentBlocks:       blockTypes,
			HasThinking:         rf.HasThinkingBlock,
			HasThinkingText:     rf.HasThinkingText,
			MaxTokens:           maxTokens,
			BashCommands:        rf.BashCommands,
			HasToolResult:       extractHasToolResult(t.Request),
			MessageCount:        extractMessageCount(t.Request),
			SystemPromptSize:    extractSystemPromptSize(t.Request),
			MemoryFilesLoaded:   extractMemoryFiles(t.Request),
			MemoryOperations:    rf.MemoryOps,
		}

		// Classify
		msg.Phase = ClassifyPhase(toolsCalled, rf.BashCommands)
		msg.Dependency = ClassifyDependency(t.Request)
		msg.Complexity = ClassifyComplexity(&msg)
		msg.IsOffloadCandidate, msg.OffloadReason = ClassifyOffload(&msg)

		report.Messages = append(report.Messages, msg)

		// --- Accumulate aggregates ---

		// Cost by model
		mc, ok := modelCosts[t.Model]
		if !ok {
			mc = &ModelCost{Model: t.Model}
			modelCosts[t.Model] = mc
		}
		mc.Messages++
		mc.InputTokens += t.InputTokens
		mc.OutputTokens += t.OutputTokens
		mc.CacheReadTokens += t.CacheReadTokens
		mc.CacheCreationTokens += rf.CacheCreation
		mc.TotalCost += cost

		// Tokens by model
		mt, ok := modelTokens[t.Model]
		if !ok {
			mt = &ModelTokenStats{Model: t.Model}
			modelTokens[t.Model] = mt
		}
		mt.Messages++

		// Tool counts
		for _, tool := range toolsCalled {
			toolCounts[tool]++
		}

		// Block type counts
		for _, bt := range blockTypes {
			blockCounts[bt]++
		}

		// Session
		sa, ok := sessionMap[t.SessionID]
		if !ok {
			sa = &sessionAccum{
				id:        t.SessionID,
				firstSeen: t.Timestamp,
				lastSeen:  t.Timestamp,
				models:    map[string]bool{},
				phases:    map[string]int{},
			}
			sessionMap[t.SessionID] = sa
		}
		sa.messages++
		sa.totalCost += cost
		sa.models[t.Model] = true
		sa.phases[msg.Phase]++
		if t.Timestamp.Before(sa.firstSeen) {
			sa.firstSeen = t.Timestamp
		}
		if t.Timestamp.After(sa.lastSeen) {
			sa.lastSeen = t.Timestamp
		}

		// Phase & complexity
		phaseCounts[msg.Phase]++
		complexityCounts[msg.Complexity]++

		// Thinking
		if rf.HasThinkingBlock {
			thinking.MessagesWithThinking++
			if rf.HasThinkingText {
				thinking.MessagesWithText++
			} else {
				thinking.MessagesSignatureOnly++
			}
		}

		// Offload
		if msg.IsOffloadCandidate {
			offloadReasons[msg.OffloadReason]++
			offloadSavings += cost
		}

		// Context stats
		totalMsgCount += msg.MessageCount
		if msg.MessageCount > maxMsgCount {
			maxMsgCount = msg.MessageCount
		}
		totalSystemPromptSize += msg.SystemPromptSize

		if prevCount, ok := sessionLastMsgCount[t.SessionID]; ok {
			if msg.MessageCount < prevCount {
				report.Messages[len(report.Messages)-1].IsCompactionEvent = true
				compactionEvents++
			} else if msg.MessageCount > prevCount {
				contextGrowthTotal += msg.MessageCount - prevCount
				contextGrowthSteps++
			}
		}
		sessionLastMsgCount[t.SessionID] = msg.MessageCount

		// Memory files loaded
		totalMemoryFilesLoaded += len(msg.MemoryFilesLoaded)
		if len(msg.MemoryFilesLoaded) > maxMemoryFilesLoaded {
			maxMemoryFilesLoaded = len(msg.MemoryFilesLoaded)
		}

		// Memory operations
		for _, op := range msg.MemoryOperations {
			totalMemoryOps++
			fac, ok := memoryFileAccess[op.Path]
			if !ok {
				fac = &FileAccessCount{Path: op.Path}
				memoryFileAccess[op.Path] = fac
			}
			if op.Type == "read" {
				fac.Reads++
			} else {
				fac.Writes++
			}
		}

		totalCost += cost
	}

	// Finalize cost report
	report.Cost.GrandTotal = totalCost
	if len(traces) > 0 {
		report.Cost.AvgPerMsg = totalCost / float64(len(traces))
	}
	for _, mc := range modelCosts {
		p := Pricing[mc.Model]
		mc.InputCost = float64(mc.InputTokens) / 1_000_000 * p.Input
		mc.OutputCost = float64(mc.OutputTokens) / 1_000_000 * p.Output
		mc.CacheReadCost = float64(mc.CacheReadTokens) / 1_000_000 * p.CacheRead
		mc.CacheCreationCost = float64(mc.CacheCreationTokens) / 1_000_000 * p.CacheCreate
		if mc.Messages > 0 {
			mc.AvgCostPerMessage = mc.TotalCost / float64(mc.Messages)
		}
		report.Cost.ByModel = append(report.Cost.ByModel, *mc)
	}
	sort.Slice(report.Cost.ByModel, func(i, j int) bool {
		return report.Cost.ByModel[i].Model < report.Cost.ByModel[j].Model
	})

	// Finalize token stats
	for _, mt := range modelTokens {
		mc := modelCosts[mt.Model]
		n := float64(mt.Messages)
		mt.AvgInput = float64(mc.InputTokens) / n
		mt.AvgOutput = float64(mc.OutputTokens) / n
		mt.AvgCacheRead = float64(mc.CacheReadTokens) / n
		mt.AvgCacheCreation = float64(mc.CacheCreationTokens) / n
		report.Tokens.ByModel = append(report.Tokens.ByModel, *mt)
	}
	sort.Slice(report.Tokens.ByModel, func(i, j int) bool {
		return report.Tokens.ByModel[i].Model < report.Tokens.ByModel[j].Model
	})

	// Finalize tool usage
	totalToolCalls := 0
	for _, c := range toolCounts {
		totalToolCalls += c
	}
	report.Tools.TotalCalls = totalToolCalls
	for name, count := range toolCounts {
		pct := 0.0
		if totalToolCalls > 0 {
			pct = float64(count) / float64(totalToolCalls) * 100
		}
		report.Tools.Tools = append(report.Tools.Tools, ToolCount{Name: name, Count: count, Percentage: pct})
	}
	sort.Slice(report.Tools.Tools, func(i, j int) bool {
		return report.Tools.Tools[i].Count > report.Tools.Tools[j].Count
	})

	// Finalize content blocks
	totalBlocks := 0
	for _, c := range blockCounts {
		totalBlocks += c
	}
	report.Blocks.TotalBlocks = totalBlocks
	for typ, count := range blockCounts {
		pct := 0.0
		if totalBlocks > 0 {
			pct = float64(count) / float64(totalBlocks) * 100
		}
		report.Blocks.Types = append(report.Blocks.Types, BlockTypeCount{Type: typ, Count: count, Percentage: pct})
	}
	sort.Slice(report.Blocks.Types, func(i, j int) bool {
		return report.Blocks.Types[i].Count > report.Blocks.Types[j].Count
	})

	// Finalize sessions
	for _, sa := range sessionMap {
		models := make([]string, 0, len(sa.models))
		for m := range sa.models {
			models = append(models, m)
		}
		sort.Strings(models)
		dur := sa.lastSeen.Sub(sa.firstSeen)
		report.Sessions = append(report.Sessions, SessionSummary{
			SessionID:   sa.id,
			Messages:    sa.messages,
			TotalCost:   sa.totalCost,
			Duration:    dur,
			DurationStr: dur.Truncate(time.Second).String(),
			FirstSeen:   sa.firstSeen,
			LastSeen:    sa.lastSeen,
			Models:      models,
			Phases:      sa.phases,
		})
	}
	sort.Slice(report.Sessions, func(i, j int) bool {
		return report.Sessions[i].Messages > report.Sessions[j].Messages
	})

	// Finalize thinking
	report.Thinking = thinking

	// Finalize phase/complexity breakdowns
	report.Phases = buildBreakdown(phaseCounts, len(traces))
	report.Complexity = buildBreakdown(complexityCounts, len(traces))

	// Finalize offload
	report.Offload.TotalCandidates = 0
	report.Offload.PotentialSavings = offloadSavings
	for reason, count := range offloadReasons {
		report.Offload.TotalCandidates += count
		pct := 0.0
		if len(traces) > 0 {
			pct = float64(count) / float64(len(traces)) * 100
		}
		report.Offload.ByReason = append(report.Offload.ByReason, CategoryCount{Name: reason, Count: count, Percentage: pct})
	}
	sort.Slice(report.Offload.ByReason, func(i, j int) bool {
		return report.Offload.ByReason[i].Count > report.Offload.ByReason[j].Count
	})

	// Finalize context analysis
	n := float64(len(traces))
	if n > 0 {
		report.Context.AvgMessageCount = float64(totalMsgCount) / n
		report.Context.AvgSystemPromptSize = int(float64(totalSystemPromptSize) / n)
	}
	report.Context.MaxMessageCount = maxMsgCount
	report.Context.CompactionEvents = compactionEvents
	if contextGrowthSteps > 0 {
		report.Context.ContextGrowthRate = float64(contextGrowthTotal) / float64(contextGrowthSteps)
	}

	// Finalize memory analysis
	totalReads := 0
	totalWrites := 0
	for _, fac := range memoryFileAccess {
		totalReads += fac.Reads
		totalWrites += fac.Writes
	}
	report.Memory.TotalRecalls = totalReads
	report.Memory.TotalWrites = totalWrites
	report.Memory.UniqueFilesAccessed = len(memoryFileAccess)
	for _, fac := range memoryFileAccess {
		total := fac.Reads + fac.Writes
		pct := 0.0
		if totalMemoryOps > 0 {
			pct = float64(total) / float64(totalMemoryOps) * 100
		}
		fac.Percentage = pct
		report.Memory.FileAccessCounts = append(report.Memory.FileAccessCounts, *fac)
	}
	sort.Slice(report.Memory.FileAccessCounts, func(i, j int) bool {
		a := report.Memory.FileAccessCounts[i]
		b := report.Memory.FileAccessCounts[j]
		return (a.Reads + a.Writes) > (b.Reads + b.Writes)
	})
	if n > 0 {
		report.Memory.AvgMemoryFilesLoaded = float64(totalMemoryFilesLoaded) / n
	}
	report.Memory.MaxMemoryFilesLoaded = maxMemoryFilesLoaded

	return report
}

func buildBreakdown(counts map[string]int, total int) []CategoryCount {
	var out []CategoryCount
	for name, count := range counts {
		pct := 0.0
		if total > 0 {
			pct = float64(count) / float64(total) * 100
		}
		out = append(out, CategoryCount{Name: name, Count: count, Percentage: pct})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

type sessionAccum struct {
	id        string
	messages  int
	totalCost float64
	firstSeen time.Time
	lastSeen  time.Time
	models    map[string]bool
	phases    map[string]int
}
