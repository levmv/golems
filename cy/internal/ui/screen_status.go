package ui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/levmv/golems/pkg/llm"
)

const maxExitTranscriptLines = 200

func (m cyTUIModel) footerMetaLine() string {
	parts := []string{}
	if m.cfg.ModelURI != "" {
		parts = append(parts, sanitizeTerminalText(m.cfg.ModelURI))
	}
	if profile := m.agent.CurrentProfile(); profile != "" {
		parts = append(parts, sanitizeTerminalText(profile))
	} else if m.cfg.CapabilityProfile != "" {
		parts = append(parts, sanitizeTerminalText(m.cfg.CapabilityProfile))
	}
	pathIndex := -1
	if m.root != "" {
		pathIndex = len(parts)
		parts = append(parts, sanitizeTerminalText(m.root))
	}
	if status := compactContextStatus(m.agent.CachedContextReport()); status != "" {
		parts = append(parts, status)
	}
	currentUsage, err := m.agent.SessionUsage()
	if err != nil {
		currentUsage = llm.Usage{}
	}
	if usage := compactUsage(currentUsage); usage != "" {
		parts = append(parts, usage)
	}
	if !m.viewport.AtBottom() {
		parts = append(parts, fmt.Sprintf("scroll %.0f%%", m.viewport.ScrollPercent()*100))
	}
	if pathIndex >= 0 {
		const separator = " · "
		fixedWidth := visibleLen(separator) * (len(parts) - 1)
		for index, part := range parts {
			if index != pathIndex {
				fixedWidth += visibleLen(part)
			}
		}
		parts[pathIndex] = truncateMiddle(parts[pathIndex], max(1, m.lineWidth()-fixedWidth))
	}
	return m.muted(strings.Join(parts, " · "))
}

func (m cyTUIModel) workingIndicatorLine() string {
	if !m.working {
		return ""
	}
	line := m.spinner.View() + " Working (" + formatTurnDuration(m.turnElapsed(time.Now())) + " • esc to interrupt)"
	return strings.Repeat(" ", transcriptGutter) + truncateANSI(m.muted(line), m.contentWidth())
}

func (m cyTUIModel) turnElapsed(now time.Time) time.Duration {
	if m.turnStartedAt.IsZero() {
		return 0
	}
	return max(time.Duration(0), now.Sub(m.turnStartedAt))
}

func (m *cyTUIModel) finishTurnDuration(now time.Time) {
	if m.turnStartedAt.IsZero() {
		return
	}
	duration := m.turnElapsed(now)
	m.turnStartedAt = time.Time{}
	m.blocks = append(m.blocks, screenBlock{kind: screenBlockTurnDuration, turnDuration: duration})
}

func formatTurnDuration(duration time.Duration) string {
	seconds := max(int64(0), int64(duration/time.Second))
	hours := seconds / 3600
	minutes := seconds % 3600 / 60
	seconds %= 60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func truncateMiddle(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if visibleLen(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	remaining := width - 1
	left := remaining / 2
	right := remaining - left
	total := visibleLen(text)
	return ansi.Cut(text, 0, left) + "…" + ansi.Cut(text, total-right, total)
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (m cyTUIModel) muted(text string) string {
	if !m.console.useStyle || text == "" {
		return text
	}
	return m.mutedStyle.Render(text)
}

func (m cyTUIModel) selection(text string) string {
	if !m.console.useStyle || text == "" {
		return text
	}
	return m.selectionStyle.Render(text)
}

func (m cyTUIModel) accent(text string) string {
	if !m.console.useStyle || text == "" {
		return text
	}
	return m.accentStyle.Render(text)
}

func (m cyTUIModel) error(text string) string {
	if !m.console.useStyle || text == "" {
		return text
	}
	return m.errorStyle.Render(text)
}

func (m cyTUIModel) success(text string) string {
	if !m.console.useStyle || text == "" {
		return text
	}
	return m.successStyle.Render(text)
}

func compactUsage(usage llm.Usage) string {
	if usage.PromptTokens == 0 && usage.CachedTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return ""
	}
	parts := []string{"↑" + compactTokenCount(usage.PromptTokens)}
	if usage.CachedTokens > 0 {
		parts = append(parts, "↻"+compactTokenCount(usage.CachedTokens))
	}
	parts = append(parts, "↓"+compactTokenCount(usage.CompletionTokens))
	return strings.Join(parts, " ")
}

func compactTokenCount(count int) string {
	if count < 1_000 {
		return fmt.Sprintf("%d", count)
	}
	divisor := 1_000
	suffix := "k"
	if count >= 1_000_000_000 {
		divisor = 1_000_000_000
		suffix = "b"
	} else if count >= 1_000_000 {
		divisor = 1_000_000
		suffix = "m"
	}
	whole := count / divisor
	tenths := count % divisor / (divisor / 10)
	if tenths == 0 {
		return fmt.Sprintf("%d%s", whole, suffix)
	}
	return fmt.Sprintf("%d.%d%s", whole, tenths, suffix)
}

func printExitTranscript(out io.Writer, model cyTUIModel) {
	model.working = false
	lines := model.renderTranscriptLines()
	if len(lines) == 0 {
		return
	}
	if len(lines) > maxExitTranscriptLines {
		omitted := len(lines) - maxExitTranscriptLines + 1
		tail := append([]string(nil), lines[len(lines)-(maxExitTranscriptLines-1):]...)
		lines = append([]string{fmt.Sprintf("  … %d earlier transcript lines omitted", omitted)}, tail...)
	}
	fmt.Fprintln(out)
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
}

func visibleLen(s string) int {
	return lipgloss.Width(s)
}

func truncateANSI(s string, width int) string {
	if width <= 0 || visibleLen(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}
