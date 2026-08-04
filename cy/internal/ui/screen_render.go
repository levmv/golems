package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

func (m *cyTUIModel) renderTranscriptLines() []string {
	lines, _ := m.renderTranscriptLinesFromDirty()
	return lines
}

func (m *cyTUIModel) renderTranscriptLinesFromDirty() ([]string, int) {
	styled := m.console != nil && m.console.useStyle
	if m.renderCacheWidth != m.lineWidth() || m.renderCacheStyled != styled || len(m.renderCache) > len(m.blocks) {
		m.renderCache = nil
		m.renderCacheLines = nil
		m.renderCacheWidth = m.lineWidth()
		m.renderCacheStyled = styled
		m.renderDirtyFrom = 0
	}
	common := min(m.renderDirtyFrom, len(m.renderCache), len(m.blocks))
	lineEnd := 0
	if common > 0 {
		lineEnd = m.renderCache[common-1].end
	}
	m.renderCache = m.renderCache[:common]
	m.renderCacheLines = m.renderCacheLines[:lineEnd]
	for index := common; index < len(m.blocks); index++ {
		block := m.blocks[index]
		var previous screenBlockKind
		havePrevious := index > 0
		if havePrevious {
			previous = m.blocks[index-1].kind
		}
		m.renderCacheLines = append(m.renderCacheLines, m.renderBlockLines(block, previous, havePrevious)...)
		m.renderCache = append(m.renderCache, renderedScreenBlock{block: block, end: len(m.renderCacheLines)})
	}
	m.renderDirtyFrom = len(m.blocks)
	return m.renderCacheLines, lineEnd
}

func (m cyTUIModel) renderPicker() []string {
	selected := m.picker.index
	limit := m.replacementPickerCapacity()
	if limit == 0 {
		return nil
	}
	start := max(0, selected-limit/2)
	if start+limit > len(m.picker.items) {
		start = max(0, len(m.picker.items)-limit)
	}
	end := min(len(m.picker.items), start+limit)

	lines := make([]string, 0, limit)
	section := ""
	for index := start; index < end; index++ {
		item := m.picker.items[index]
		if item.section != "" && item.section != section {
			if len(lines) == limit {
				break
			}
			section = item.section
			lines = append(lines, m.renderMarkedLine(" ", m.muted(sanitizeTerminalText(section)), m.mutedStyle))
		}
		if len(lines) == limit {
			break
		}
		label := sanitizeTerminalText(item.label)
		if item.current {
			label = m.accent(label)
		} else if index == selected {
			label = m.selection(label)
		}
		detail := label
		if item.current && m.picker.kind != pickerLogin {
			detail += m.muted("  current")
		}
		if item.description != "" && (m.picker.kind != pickerModel || index == selected) {
			detail += m.muted("  " + sanitizeTerminalText(item.description))
		}
		marker := " "
		if index == selected {
			marker = "›"
		}
		lines = append(lines, m.renderMarkedLine(marker, truncateANSI(detail, m.contentWidth()), m.accentStyle))
	}
	return lines
}

func (m cyTUIModel) replacementPickerCapacity() int {
	const maxItems = 10
	if m.height <= 0 {
		return maxItems
	}
	// The picker replaces the editor but always leaves one transcript row.
	available := m.height - 1 - screenFixedRows
	return min(maxItems, max(0, available))
}

func (m *cyTUIModel) renderCommandSuggestions() []string {
	if !m.commandSuggestionsVisible() {
		return nil
	}
	matches := m.commandSuggestions
	selected := m.commandSuggestionIndex
	limit := m.commandSuggestionCapacity()
	if limit == 0 {
		return nil
	}
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > len(matches) {
		start = max(0, len(matches)-limit)
	}
	end := min(len(matches), start+limit)

	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		marker := " "
		if index == selected {
			marker = "›"
		}
		detail := matches[index]
		if description := commandDescription(matches[index]); description != "" {
			detail = fmt.Sprintf("%-10s %s", matches[index], description)
		}
		lines = append(lines, m.renderMarkedLine(marker, truncateANSI(detail, m.contentWidth()), m.accentStyle))
	}
	return lines
}

func (m cyTUIModel) commandSuggestionCapacity() int {
	const maxItems = 7
	if m.height <= 0 {
		return maxItems
	}
	// Keep room for one transcript row, the editor, and the fixed footer rows.
	available := m.height - 1 - m.editorHeight() - screenFixedRows
	return min(maxItems, max(0, available))
}

func commandDescription(name string) string {
	for _, command := range tuiCommands {
		if command.name() == name {
			return command.description
		}
	}
	switch {
	case strings.HasPrefix(name, "/profile "):
		return "switch capability profile"
	case strings.HasPrefix(name, "/login "):
		return "manage this provider credential"
	case strings.HasPrefix(name, "/logout "):
		return "remove this stored provider credential"
	case strings.HasPrefix(name, "/model "):
		return ""
	}
	return ""
}

func (m cyTUIModel) renderBlockLines(block screenBlock, previous screenBlockKind, havePrevious bool) []string {
	var lines []string
	if block.kind == screenBlockTool && havePrevious && previous != screenBlockTool {
		lines = append(lines, m.rawMarkedLine(" ", ""))
	}
	switch block.kind {
	case screenBlockBanner:
		lines = append(lines, m.renderMarkedLine(" ", m.accent("Cy")+"  "+m.muted("Type / for commands, ! for shell."), m.mutedStyle))
	case screenBlockSystem:
		for _, line := range strings.Split(block.text, "\n") {
			lines = append(lines, m.renderWrappedMarkedLine(" ", m.muted(line), m.mutedStyle)...)
		}
	case screenBlockInfo:
		lines = append(lines, m.rawMarkedLine(" ", ""))
		for _, line := range strings.Split(block.text, "\n") {
			lines = append(lines, m.renderWrappedMarkedLine(" ", line, m.mutedStyle)...)
		}
	case screenBlockUser:
		lines = append(lines, m.renderUserBlock(block.text)...)
	case screenBlockAssistant:
		lines = append(lines, m.renderAssistantBlock(block.text)...)
	case screenBlockTool:
		if block.fileChange != nil {
			lines = append(lines, m.renderFileChangeLines(block.text, *block.fileChange)...)
		} else if block.processResult != nil {
			lines = append(lines, m.renderProcessResultLines(block.text, *block.processResult, !block.userInitiated)...)
			if block.userInitiated {
				lines = append(lines, m.renderShellOutputLines(block.processOutput)...)
			}
		} else if block.toolName == "bash" && !block.toolStartedAt.IsZero() {
			lines = append(lines, m.renderPendingProcessLines(block.text, block.toolElapsedMillis)...)
		} else {
			lines = append(lines, m.renderToolLines(block.text)...)
		}
	case screenBlockError:
		lines = append(lines, m.renderWrappedMarkedLine("•", block.text, m.errorStyle)...)
	case screenBlockTurnDuration:
		lines = append(lines, m.renderTurnDurationLine(block.turnDuration))
	}
	return lines
}

func (m cyTUIModel) renderTurnDurationLine(duration time.Duration) string {
	line := "─ Worked for " + formatTurnDuration(duration) + " "
	width := m.contentWidth()
	if visibleLen(line) < width {
		line += strings.Repeat("─", width-visibleLen(line))
	} else {
		line = truncateANSI(line, width)
	}
	return m.renderMarkedLine(" ", m.muted(line), m.mutedStyle)
}

func (m cyTUIModel) renderUserBlock(text string) []string {
	contentWidth := m.contentWidth()
	if !m.console.useStyle {
		lines := []string{m.rawMarkedLine(" ", "")}
		marked := false
		for _, textLine := range strings.Split(text, "\n") {
			wrapped := wrapDisplayLine(textLine, contentWidth)
			for _, line := range wrapped {
				marker := " "
				if !marked {
					marker = userMarker
					marked = true
				}
				lines = append(lines, m.rawMarkedLine(marker, line))
			}
		}
		return append(lines, m.rawMarkedLine(" ", ""))
	}

	blank := m.renderMarkedLine(" ", m.userBackgroundLine("", contentWidth), m.mutedStyle)
	lines := []string{m.rawMarkedLine(" ", ""), blank}
	marked := false
	textWidth := max(1, contentWidth-1)
	for _, textLine := range strings.Split(text, "\n") {
		wrapped := wrapDisplayLine(textLine, textWidth)
		for _, line := range wrapped {
			marker := " "
			if !marked {
				marker = userMarker
				marked = true
			}
			lines = append(lines, m.renderMarkedLine(marker, m.userBackgroundLine(line, contentWidth), m.mutedStyle))
		}
	}
	return append(lines, blank)
}
func (m cyTUIModel) contentWidth() int {
	width := m.lineWidth() - transcriptGutter
	if width < 1 {
		return 1
	}
	return width
}

func (m cyTUIModel) userBackgroundLine(text string, width int) string {
	if width < 1 {
		width = 1
	}
	line := " " + text
	if visibleLen(line) > width {
		line = truncateANSI(line, width)
	}
	if pad := width - visibleLen(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return m.userStyle.Render(line)
}

func (m cyTUIModel) renderAssistantBlock(text string) []string {
	lines := []string{m.rawMarkedLine(" ", "")}
	marked := false
	for _, line := range m.console.RenderMarkdownLinesAtWidth(text, m.contentWidth()) {
		marker := " "
		if !marked && strings.TrimSpace(line) != "" {
			marker = "•"
			marked = true
		}
		lines = append(lines, m.renderWrappedMarkedLine(marker, line, m.mutedStyle)...)
	}
	return append(lines, m.rawMarkedLine(" ", ""))
}

func (m cyTUIModel) renderToolLines(text string) []string {
	style := m.successStyle
	if strings.Contains(strings.ToLower(text), "error") {
		style = m.errorStyle
	}
	return m.renderWrappedMarkedLine("•", m.renderToolDisplay(text), style)
}

func (m cyTUIModel) renderToolDisplay(text string) string {
	name, arguments := splitToolDisplay(text)
	return m.accent(name) + arguments
}

func (m cyTUIModel) renderProcessResultLines(command string, result processResultMeta, showFailureTail bool) []string {
	marker := "×"
	style := m.errorStyle
	if processRunning(result.Status) {
		marker = "◌"
		style = m.mutedStyle
	} else if processSucceeded(result.Status) {
		marker = "✓"
		style = m.successStyle
	}
	line := m.renderToolDisplay(command)
	if detail := processStatusText(result); detail != "" {
		line += "  " + m.muted(detail)
	}
	lines := m.renderWrappedMarkedLine(marker, line, style)
	if showFailureTail && !processSucceeded(result.Status) && !processRunning(result.Status) {
		for _, tail := range processFailureTailLines(result.FailureTail, 6) {
			lines = append(lines, m.renderWrappedMarkedLine(" ", m.error(tail), m.mutedStyle)...)
		}
	}
	return lines
}

func (m cyTUIModel) renderShellOutputLines(output string) []string {
	if output = sanitizeTerminalText(output); output == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		lines = append(lines, m.renderWrappedMarkedLine(" ", line, m.mutedStyle)...)
	}
	return lines
}

func (m cyTUIModel) renderPendingProcessLines(command string, elapsedMillis int64) []string {
	detail := formatProcessDuration(time.Duration(elapsedMillis) * time.Millisecond)
	return m.renderWrappedMarkedLine("◌", m.renderToolDisplay(command)+"  "+m.muted(detail), m.mutedStyle)
}

func (m cyTUIModel) renderFileChangeLines(summary string, change fileChangeMeta) []string {
	stats := []string{}
	if change.Additions > 0 {
		stats = append(stats, m.success(fmt.Sprintf("+%d", change.Additions)))
	}
	if change.Deletions > 0 {
		stats = append(stats, m.error(fmt.Sprintf("−%d", change.Deletions)))
	}
	styledSummary := m.renderToolDisplay(summary)
	if len(stats) > 0 {
		styledSummary += "  " + strings.Join(stats, " ")
	} else {
		styledSummary += "  " + m.muted("no changes")
	}
	lines := m.renderWrappedMarkedLine("•", styledSummary, m.successStyle)

	oldWidth, newWidth := diffNumberWidths(change)
	for _, hunk := range change.Hunks {
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)
		lines = append(lines, m.renderWrappedMarkedLine(" ", m.muted(header), m.mutedStyle)...)
		for _, line := range hunk.Lines {
			oldNumber := diffLineNumber(line.OldLine)
			newNumber := diffLineNumber(line.NewLine)
			gutter := fmt.Sprintf("%*s %*s │ ", oldWidth, oldNumber, newWidth, newNumber)
			marker := " "
			markerStyle := m.mutedStyle
			content := sanitizeTerminalText(line.Text)
			switch line.Kind {
			case "add":
				marker = "+"
				markerStyle = m.successStyle
				content = m.success(content)
			case "delete":
				marker = "−"
				markerStyle = m.errorStyle
				content = m.error(content)
			default:
				content = m.muted(content)
			}
			if line.NoNewline {
				content += m.muted("  [no newline]")
			}
			lines = append(lines, m.renderWrappedMarkedLine(marker, m.muted(gutter)+content, markerStyle)...)
		}
	}
	if change.Truncated {
		detail := "diff preview limited"
		if change.TotalHunks > len(change.Hunks) {
			detail = fmt.Sprintf("diff preview limited · %d hunks total", change.TotalHunks)
		}
		lines = append(lines, m.renderWrappedMarkedLine("…", m.muted(detail), m.mutedStyle)...)
	}
	return lines
}

func diffLineNumber(value int) string {
	if value <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", value)
}

func (m cyTUIModel) renderWrappedMarkedLine(marker string, text string, markerStyle lipgloss.Style) []string {
	wrapped := wrapDisplayLine(text, m.contentWidth())
	lines := make([]string, 0, len(wrapped))
	for i, part := range wrapped {
		lineMarker := marker
		if i > 0 {
			lineMarker = " "
		}
		lines = append(lines, m.renderMarkedLine(lineMarker, part, markerStyle))
	}
	return lines
}

func wrapDisplayLine(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	wrapped := lipgloss.Wrap(text, width, "")
	if wrapped == "" {
		return []string{""}
	}
	return strings.Split(wrapped, "\n")
}

func (m cyTUIModel) renderMarkedLine(marker string, text string, markerStyle lipgloss.Style) string {
	if !m.console.useStyle {
		return m.rawMarkedLine(marker, text)
	}
	return markerStyle.Render(marker) + strings.Repeat(" ", transcriptGutter-1) + text
}

func (m cyTUIModel) rawMarkedLine(marker string, text string) string {
	return marker + strings.Repeat(" ", transcriptGutter-1) + text
}
