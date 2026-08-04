package ui

import (
	"fmt"
	"strings"
	"time"

	toolruntime "github.com/levmv/golems/cy/internal/tools"
)

const (
	processResultMetaType = toolruntime.ProcessResultMetaType
	jobRunning            = toolruntime.JobRunning
	jobCompleted          = toolruntime.JobCompleted
	jobFailed             = toolruntime.JobFailed
	jobKilled             = toolruntime.JobKilled
	jobTimedOut           = toolruntime.JobTimedOut
)

// processResultMeta keeps process presentation independent from the textual
// result intended for the model and is also emitted in JSON mode.
type processResultMeta = toolruntime.ProcessResultMeta

func processResultMetaFrom(value any) (processResultMeta, bool) {
	return toolruntime.ProcessResultMetaFrom(value)
}

func processStatusText(meta processResultMeta) string {
	parts := []string{formatProcessDuration(time.Duration(meta.DurationMillis) * time.Millisecond)}
	if meta.Status == jobRunning {
		parts = append(parts, "running", meta.JobID)
	} else {
		switch meta.Status {
		case jobTimedOut:
			parts = append(parts, "timed out")
		case jobKilled:
			parts = append(parts, "killed")
		case jobFailed:
			if meta.ExitCode == nil {
				parts = append(parts, "failed")
			}
		}
		if meta.ExitCode != nil {
			parts = append(parts, fmt.Sprintf("exit %d", *meta.ExitCode))
		}
		if meta.OutputBytes > 0 {
			parts = append(parts, formatByteCount(meta.OutputBytes))
		}
		if meta.DiscardedBytes > 0 {
			parts = append(parts, fmt.Sprintf("%s discarded", formatByteCount(meta.DiscardedBytes)))
		}
	}
	return strings.Join(parts, " · ")
}

func formatProcessDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Second {
		return fmt.Sprintf("%dms", duration.Milliseconds())
	}
	if duration < 10*time.Second {
		return fmt.Sprintf("%.1fs", duration.Seconds())
	}
	if duration < time.Minute {
		return fmt.Sprintf("%.0fs", duration.Seconds())
	}
	minutes := int(duration / time.Minute)
	seconds := int(duration/time.Second) % 60
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func formatByteCount(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
}

func processSucceeded(status string) bool {
	return status == jobCompleted
}

func processCommandOutput(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if _, output, ok := strings.Cut(content, "\n\n"); ok {
		return strings.TrimSuffix(output, "\n")
	}
	return strings.TrimSuffix(content, "\n")
}

func processRunning(status string) bool {
	return status == jobRunning
}

func processFailureTailLines(text string, limit int) []string {
	if limit <= 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, compactSingleLine(line, 180))
		}
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}
