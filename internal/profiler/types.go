package profiler

import "time"

// BlockInfo captures the type and optional name of a content block.
type BlockInfo struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// MemoryOp represents a read or write operation on a memory file.
type MemoryOp struct {
	Type string `json:"type"` // "read" or "write"
	Path string `json:"path"`
}

// MessageProfile is the enriched per-message profiling record.
type MessageProfile struct {
	Index     int       `json:"index"`
	Timestamp time.Time `json:"timestamp"`
	SessionID string    `json:"session_id"`
	Model     string    `json:"model"`

	// Token counts
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	ThinkingTokens      int     `json:"thinking_tokens"`
	Cost                float64 `json:"cost"`

	// Content analysis
	OutputType      string   `json:"output_type"`
	ToolsCalled     []string `json:"tools_called"`
	ContentBlocks   []string `json:"content_blocks"`
	HasThinking     bool     `json:"has_thinking"`
	HasThinkingText bool     `json:"has_thinking_text"`
	MaxTokens       int      `json:"max_tokens"`
	BashCommands    []string `json:"bash_commands,omitempty"`

	// Context & memory
	MessageCount      int        `json:"message_count"`
	SystemPromptSize  int        `json:"system_prompt_size_bytes"`
	MemoryFilesLoaded []string   `json:"memory_files_loaded,omitempty"`
	MemoryOperations  []MemoryOp `json:"memory_operations,omitempty"`
	IsCompactionEvent bool       `json:"is_compaction_event,omitempty"`

	// Classification
	Phase              string `json:"phase"`
	Complexity         string `json:"complexity"`
	Dependency         string `json:"dependency"`
	HasToolResult      bool   `json:"has_tool_result"`
	IsOffloadCandidate bool   `json:"is_offload_candidate"`
	OffloadReason      string `json:"offload_reason,omitempty"`
}

// ProfileReport is the top-level report containing all analysis sections.
type ProfileReport struct {
	Files    []string         `json:"files"`
	Messages []MessageProfile `json:"messages"`

	Cost       CostReport          `json:"cost"`
	Tokens     TokenStats          `json:"tokens"`
	Tools      ToolUsage           `json:"tools"`
	Blocks     ContentBlockAnalysis `json:"content_blocks"`
	Sessions   []SessionSummary    `json:"sessions"`
	Thinking   ThinkingAnalysis    `json:"thinking"`
	Phases     []CategoryCount     `json:"phases"`
	Complexity []CategoryCount     `json:"complexity"`
	Offload    OffloadAnalysis     `json:"offload"`
	Context    ContextAnalysis     `json:"context"`
	Memory     MemoryAnalysis      `json:"memory"`
}

// ModelCost holds cost breakdown for a single model.
type ModelCost struct {
	Model               string  `json:"model"`
	Messages            int     `json:"messages"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	InputCost           float64 `json:"input_cost"`
	OutputCost          float64 `json:"output_cost"`
	CacheReadCost       float64 `json:"cache_read_cost"`
	CacheCreationCost   float64 `json:"cache_creation_cost"`
	TotalCost           float64 `json:"total_cost"`
	AvgCostPerMessage   float64 `json:"avg_cost_per_message"`
}

// CostReport aggregates costs across all models.
type CostReport struct {
	ByModel    []ModelCost `json:"by_model"`
	GrandTotal float64     `json:"grand_total"`
	AvgPerMsg  float64     `json:"avg_per_message"`
}

// ModelTokenStats holds average token counts for a single model.
type ModelTokenStats struct {
	Model            string  `json:"model"`
	Messages         int     `json:"messages"`
	AvgInput         float64 `json:"avg_input"`
	AvgOutput        float64 `json:"avg_output"`
	AvgCacheRead     float64 `json:"avg_cache_read"`
	AvgCacheCreation float64 `json:"avg_cache_creation"`
}

// TokenStats aggregates token averages by model.
type TokenStats struct {
	ByModel []ModelTokenStats `json:"by_model"`
}

// ToolCount holds usage count for a single tool.
type ToolCount struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

// ToolUsage aggregates tool call statistics.
type ToolUsage struct {
	TotalCalls int         `json:"total_calls"`
	Tools      []ToolCount `json:"tools"`
}

// BlockTypeCount holds count for a single content block type.
type BlockTypeCount struct {
	Type       string  `json:"type"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

// ContentBlockAnalysis aggregates content block type statistics.
type ContentBlockAnalysis struct {
	TotalBlocks int              `json:"total_blocks"`
	Types       []BlockTypeCount `json:"types"`
}

// SessionSummary holds per-session statistics.
type SessionSummary struct {
	SessionID   string         `json:"session_id"`
	Messages    int            `json:"messages"`
	TotalCost   float64        `json:"total_cost"`
	Duration    time.Duration  `json:"-"`
	DurationStr string         `json:"duration"`
	FirstSeen   time.Time      `json:"first_seen"`
	LastSeen    time.Time      `json:"last_seen"`
	Models      []string       `json:"models"`
	Phases      map[string]int `json:"phases"`
}

// ThinkingAnalysis summarizes thinking block usage.
type ThinkingAnalysis struct {
	TotalMessages         int `json:"total_messages"`
	MessagesWithThinking  int `json:"messages_with_thinking"`
	MessagesWithText      int `json:"messages_with_thinking_text"`
	MessagesSignatureOnly int `json:"messages_signature_only"`
}

// CategoryCount is a generic name+count+percentage tuple for breakdowns.
type CategoryCount struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

// OffloadAnalysis summarizes offload candidate statistics.
type OffloadAnalysis struct {
	TotalCandidates  int             `json:"total_candidates"`
	PotentialSavings float64         `json:"potential_savings"`
	ByReason         []CategoryCount `json:"by_reason"`
}

// ContextAnalysis summarizes chat history and context window patterns.
type ContextAnalysis struct {
	AvgMessageCount     float64 `json:"avg_message_count"`
	MaxMessageCount     int     `json:"max_message_count"`
	AvgSystemPromptSize int     `json:"avg_system_prompt_size_bytes"`
	CompactionEvents    int     `json:"compaction_events"`
	ContextGrowthRate   float64 `json:"avg_context_growth_per_turn"`
}

// MemoryAnalysis summarizes memory file recall and write patterns.
type MemoryAnalysis struct {
	TotalRecalls         int               `json:"total_recalls"`
	TotalWrites          int               `json:"total_writes"`
	UniqueFilesAccessed  int               `json:"unique_files_accessed"`
	FileAccessCounts     []FileAccessCount `json:"file_access_counts"`
	AvgMemoryFilesLoaded float64           `json:"avg_memory_files_loaded"`
	MaxMemoryFilesLoaded int               `json:"max_memory_files_loaded"`
}

// FileAccessCount holds read/write counts for a single memory file.
type FileAccessCount struct {
	Path       string  `json:"path"`
	Reads      int     `json:"reads"`
	Writes     int     `json:"writes"`
	Percentage float64 `json:"percentage"`
}
