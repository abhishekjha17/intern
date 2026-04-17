package profiler

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// RenderText writes a human-readable text report to w.
func RenderText(w io.Writer, r *ProfileReport) {
	renderCost(w, r)
	renderTokens(w, r)
	renderTools(w, r)
	renderBlocks(w, r)
	renderPhases(w, r)
	renderComplexity(w, r)
	renderSessions(w, r)
	renderThinking(w, r)
	renderOffload(w, r)
	renderContext(w, r)
	renderMemory(w, r)
}

func renderCost(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Cost Report ===\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "MODEL\tMSGS\tINPUT\tOUTPUT\tCACHE READ\tCACHE CREATE\tTOTAL\tAVG/MSG\n")
	fmt.Fprintf(tw, "-----\t----\t-----\t------\t----------\t------------\t-----\t-------\n")
	for _, mc := range r.Cost.ByModel {
		fmt.Fprintf(tw, "%s\t%d\t$%.4f\t$%.4f\t$%.4f\t$%.4f\t$%.4f\t$%.4f\n",
			mc.Model, mc.Messages,
			mc.InputCost, mc.OutputCost, mc.CacheReadCost, mc.CacheCreationCost,
			mc.TotalCost, mc.AvgCostPerMessage)
	}
	tw.Flush()
	fmt.Fprintf(w, "\nGrand Total: $%.4f  |  Avg per Message: $%.4f  |  Messages: %d\n\n",
		r.Cost.GrandTotal, r.Cost.AvgPerMsg, len(r.Messages))
}

func renderTokens(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Token Averages by Model ===\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "MODEL\tMSGS\tAVG INPUT\tAVG OUTPUT\tAVG CACHE READ\tAVG CACHE CREATE\n")
	fmt.Fprintf(tw, "-----\t----\t---------\t----------\t--------------\t----------------\n")
	for _, mt := range r.Tokens.ByModel {
		fmt.Fprintf(tw, "%s\t%d\t%.0f\t%.0f\t%.0f\t%.0f\n",
			mt.Model, mt.Messages,
			mt.AvgInput, mt.AvgOutput, mt.AvgCacheRead, mt.AvgCacheCreation)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderTools(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Tool Usage (%d total calls) ===\n\n", r.Tools.TotalCalls)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "TOOL\tCOUNT\t%%\tBAR\n")
	fmt.Fprintf(tw, "----\t-----\t-\t---\n")
	for _, tc := range r.Tools.Tools {
		bar := strings.Repeat("█", int(tc.Percentage/2))
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\t%s\n", tc.Name, tc.Count, tc.Percentage, bar)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderBlocks(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Content Block Types (%d total) ===\n\n", r.Blocks.TotalBlocks)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "TYPE\tCOUNT\t%%\n")
	fmt.Fprintf(tw, "----\t-----\t-\n")
	for _, bt := range r.Blocks.Types {
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\n", bt.Type, bt.Count, bt.Percentage)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderPhases(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Phase Breakdown ===\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "PHASE\tCOUNT\t%%\n")
	fmt.Fprintf(tw, "-----\t-----\t-\n")
	for _, p := range r.Phases {
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\n", p.Name, p.Count, p.Percentage)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderComplexity(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Complexity Breakdown ===\n\n")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "COMPLEXITY\tCOUNT\t%%\n")
	fmt.Fprintf(tw, "----------\t-----\t-\n")
	for _, c := range r.Complexity {
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\n", c.Name, c.Count, c.Percentage)
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderSessions(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Sessions (%d) ===\n\n", len(r.Sessions))
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "SESSION\tMSGS\tCOST\tDURATION\tMODELS\n")
	fmt.Fprintf(tw, "-------\t----\t----\t--------\t------\n")
	for _, s := range r.Sessions {
		sid := s.SessionID
		if len(sid) > 12 {
			sid = sid[:12]
		}
		fmt.Fprintf(tw, "%s\t%d\t$%.4f\t%s\t%s\n",
			sid, s.Messages, s.TotalCost,
			s.Duration.Truncate(time.Second).String(),
			strings.Join(s.Models, ", "))
	}
	tw.Flush()
	fmt.Fprintln(w)
}

func renderThinking(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Thinking Analysis ===\n\n")
	th := r.Thinking
	fmt.Fprintf(w, "Total Messages:          %d\n", th.TotalMessages)
	fmt.Fprintf(w, "With Thinking Block:     %d\n", th.MessagesWithThinking)
	fmt.Fprintf(w, "  With Thinking Text:    %d\n", th.MessagesWithText)
	fmt.Fprintf(w, "  Signature Only:        %d\n", th.MessagesSignatureOnly)
	fmt.Fprintln(w)
}

func renderOffload(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Offload Candidates ===\n\n")
	fmt.Fprintf(w, "Total Candidates: %d / %d messages\n", r.Offload.TotalCandidates, len(r.Messages))
	fmt.Fprintf(w, "Potential Savings: $%.4f\n\n", r.Offload.PotentialSavings)
	if len(r.Offload.ByReason) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "REASON\tCOUNT\t%%\n")
		fmt.Fprintf(tw, "------\t-----\t-\n")
		for _, or := range r.Offload.ByReason {
			fmt.Fprintf(tw, "%s\t%d\t%.1f%%\n", or.Name, or.Count, or.Percentage)
		}
		tw.Flush()
	}
	fmt.Fprintln(w)
}

func renderContext(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Context Window Analysis ===\n\n")
	c := r.Context
	fmt.Fprintf(w, "Avg Message Count:       %.1f\n", c.AvgMessageCount)
	fmt.Fprintf(w, "Max Message Count:       %d\n", c.MaxMessageCount)
	fmt.Fprintf(w, "Avg System Prompt Size:  %d bytes\n", c.AvgSystemPromptSize)
	fmt.Fprintf(w, "Compaction Events:       %d\n", c.CompactionEvents)
	fmt.Fprintf(w, "Avg Context Growth/Turn: %.1f messages\n", c.ContextGrowthRate)
	fmt.Fprintln(w)
}

func renderMemory(w io.Writer, r *ProfileReport) {
	fmt.Fprintf(w, "=== Memory Analysis ===\n\n")
	m := r.Memory
	fmt.Fprintf(w, "Total Recalls (reads):    %d\n", m.TotalRecalls)
	fmt.Fprintf(w, "Total Writes:             %d\n", m.TotalWrites)
	fmt.Fprintf(w, "Unique Files Accessed:    %d\n", m.UniqueFilesAccessed)
	fmt.Fprintf(w, "Avg Memory Files/Request: %.1f\n", m.AvgMemoryFilesLoaded)
	fmt.Fprintf(w, "Max Memory Files Loaded:  %d\n\n", m.MaxMemoryFilesLoaded)

	if len(m.FileAccessCounts) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "PATH\tREADS\tWRITES\t%%\n")
		fmt.Fprintf(tw, "----\t-----\t------\t-\n")
		for _, f := range m.FileAccessCounts {
			path := f.Path
			if len(path) > 50 {
				path = "..." + path[len(path)-47:]
			}
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f%%\n", path, f.Reads, f.Writes, f.Percentage)
		}
		tw.Flush()
	}
	fmt.Fprintln(w)
}
