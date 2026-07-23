package ui

import (
	"fmt"
	"strings"

	"github.com/levmv/golems/cy/internal/engine"
)

func compactContextStatus(report engine.ContextReport) string {
	if report.Window <= 0 {
		return ""
	}
	return fmt.Sprintf("context ~%d%%", report.PercentLeft)
}

func formatContextReport(report engine.ContextReport) string {
	inputLimit := fmt.Sprintf("%d", report.InputLimit)
	if report.Estimated {
		inputLimit = "~" + inputLimit
	}
	lines := []string{
		fmt.Sprintf("model: %s", report.ModelURI),
		fmt.Sprintf("context: ~%d / %s tokens; ~%d available (%d%%)", report.TotalInputTokens, inputLimit, report.AvailableInputTokens, report.PercentLeft),
		fmt.Sprintf("breakdown: system=%d tools=%d instructions=%d summary=%d history=%d pending=%d", report.SystemTokens, report.ToolTokens, report.InstructionTokens, report.SummaryTokens, report.HistoryTokens, report.PendingTokens),
		fmt.Sprintf("compactions: %d", report.CompactionCount),
	}
	return strings.Join(lines, "\n")
}
