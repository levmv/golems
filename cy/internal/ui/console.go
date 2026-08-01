package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/levmv/golems/pkg/golem"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiGray   = "\x1b[90m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

type Console struct {
	out                io.Writer
	useStyle           bool
	pendingCompactTool *compactToolBatch
	changedPaths       []string
}

func NewConsole(out io.Writer) *Console {
	if out == nil {
		out = os.Stdout
	}
	return &Console{out: out, useStyle: shouldUseStyle(out)}
}

func shouldUseStyle(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CY_COLOR"))) {
	case "always", "1", "true", "yes", "on":
		return true
	case "never", "0", "false", "no", "off":
		return false
	}
	file, ok := out.(*os.File)
	return ok && isTerminalFile(file)
}

func (c *Console) PrintRetry(text string) {
	fmt.Fprintf(c.out, "\n%s %s\n", c.style("retry:", ansiYellow), sanitizeTerminalText(text))
}

func (c *Console) PrintStatus(text string) {
	c.FlushCompactToolEvents()
	fmt.Fprintf(c.out, "\n[status] %s\n", sanitizeTerminalText(text))
}

func (c *Console) PrintDiscardedAttempt() {
	c.FlushCompactToolEvents()
	fmt.Fprintln(c.out, "\n[discarded partial model attempt]")
}

func (c *Console) PrintMarkdown(text string) {
	lines := c.RenderMarkdownLines(text)
	for i, line := range lines {
		if i > 0 {
			fmt.Fprintln(c.out)
		}
		fmt.Fprint(c.out, line)
	}
	if strings.HasSuffix(text, "\n") {
		fmt.Fprintln(c.out)
	}
}

func (c *Console) RenderMarkdownLines(text string) []string {
	text = sanitizeTerminalText(text)
	if text == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(text, "\n")
	if trimmed == "" {
		return []string{""}
	}
	input := strings.Split(trimmed, "\n")
	var fence markdownFence
	return c.renderMarkdownLines(input, &fence)
}

type markdownFence struct {
	marker byte
	length int
}

func (f markdownFence) active() bool {
	return f.marker != 0 && f.length >= 3
}

func openingMarkdownFence(line string) (markdownFence, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 || trimmed[0] != '`' && trimmed[0] != '~' {
		return markdownFence{}, false
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	if length < 3 {
		return markdownFence{}, false
	}
	if marker == '`' && strings.Contains(trimmed[length:], "`") {
		return markdownFence{}, false
	}
	return markdownFence{marker: marker, length: length}, true
}

func closesMarkdownFence(line string, fence markdownFence) bool {
	if !fence.active() {
		return false
	}
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	length := 0
	for length < len(trimmed) && trimmed[length] == fence.marker {
		length++
	}
	return length >= fence.length && strings.TrimSpace(trimmed[length:]) == ""
}

func (c *Console) renderMarkdownLines(input []string, fence *markdownFence) []string {
	out := make([]string, 0, len(input))
	for i := 0; i < len(input); {
		if fence.active() {
			if closesMarkdownFence(input[i], *fence) {
				*fence = markdownFence{}
			} else {
				out = append(out, c.renderCodeBlockLine(input[i]))
			}
			i++
			continue
		}
		if opened, ok := openingMarkdownFence(input[i]); ok {
			*fence = opened
			i++
			continue
		}
		if i+1 < len(input) && isTableSeparatorLine(input[i+1]) && strings.Contains(input[i], "|") {
			end := i + 2
			for end < len(input) && strings.Contains(input[end], "|") && strings.TrimSpace(input[end]) != "" {
				end++
			}
			out = append(out, c.renderTable(input[i:end])...)
			i = end
			continue
		}
		out = append(out, c.renderMarkdownLine(input[i]))
		i++
	}
	return out
}

func (c *Console) renderCodeBlockLine(line string) string {
	if !c.useStyle {
		return line
	}
	return c.style(line, ansiYellow)
}

func (c *Console) PrintCompactToolEvent(ev golem.StreamEvent) {
	switch ev.Kind {
	case golem.EventToolCall:
		display := describeToolCall(ev.Step.ToolName, ev.Step.Arguments)
		if display.GroupKey != "" {
			if c.pendingCompactTool != nil && c.pendingCompactTool.key == display.GroupKey {
				c.pendingCompactTool.items = append(c.pendingCompactTool.items, display.GroupItem)
				return
			}
			c.FlushCompactToolEvents()
			c.pendingCompactTool = &compactToolBatch{key: display.GroupKey, dir: display.GroupDir, items: []string{display.GroupItem}}
			return
		}
		c.FlushCompactToolEvents()
		c.printCompactToolLine(display.Text)
	case golem.EventToolResult:
		c.FlushCompactToolEvents()
		if change, ok := fileChangeMetaFrom(ev.Step.Meta); ok {
			c.recordFileChange(change)
			c.PrintFileChange(change)
		} else if result, ok := processResultMetaFrom(ev.Step.Meta); ok {
			c.PrintProcessResult(result)
		}
	case golem.EventToolError:
		c.FlushCompactToolEvents()
		message := compactSingleLine(ev.Step.Error, 180)
		if message == "" {
			message = describeToolCall(ev.Step.ToolName, ev.Step.Arguments).Text
		}
		fmt.Fprintf(c.out, "\n%s %s\n", c.style("error:", ansiRed), sanitizeTerminalText(message))
	}
}

func (c *Console) recordFileChange(change fileChangeMeta) {
	if changed, ok := changedFilePath(change); ok {
		c.changedPaths = appendUniquePath(c.changedPaths, changed)
	}
}

func (c *Console) PrintChangeSummary() {
	if summary := formatChangedFiles(c.changedPaths); summary != "" {
		fmt.Fprintf(c.out, "\n%s\n", c.muted(summary))
	}
	c.changedPaths = nil
}

func (c *Console) FlushCompactToolEvents() {
	if c.pendingCompactTool == nil {
		return
	}
	c.printCompactToolLine(formatReadGroup(c.pendingCompactTool.dir, c.pendingCompactTool.items))
	c.pendingCompactTool = nil
}

func (c *Console) printCompactToolLine(text string) {
	fmt.Fprintf(c.out, "\n%s\n", c.renderToolDisplay(sanitizeTerminalText(text)))
}

func (c *Console) renderToolDisplay(text string) string {
	name, arguments := splitToolDisplay(text)
	return c.style(name, ansiCyan) + arguments
}

func (c *Console) PrintFileChange(change fileChangeMeta) {
	stats := []string{}
	if change.Additions > 0 {
		stats = append(stats, c.style(fmt.Sprintf("+%d", change.Additions), ansiGreen))
	}
	if change.Deletions > 0 {
		stats = append(stats, c.style(fmt.Sprintf("−%d", change.Deletions), ansiRed))
	}
	summary := sanitizeTerminalText(strings.TrimSpace(change.Operation + "  " + change.Path))
	if len(stats) > 0 {
		summary = c.renderToolDisplay(summary) + "  " + strings.Join(stats, " ")
	} else {
		summary = c.renderToolDisplay(summary) + "  " + c.muted("no changes")
	}
	fmt.Fprintf(c.out, "\n%s\n", summary)

	oldWidth, newWidth := diffNumberWidths(change)
	for _, hunk := range change.Hunks {
		fmt.Fprintf(c.out, "%s\n", c.muted(fmt.Sprintf("  @@ -%d,%d +%d,%d @@", hunk.OldStart, hunk.OldLines, hunk.NewStart, hunk.NewLines)))
		for _, line := range hunk.Lines {
			oldNumber := diffLineNumber(line.OldLine)
			newNumber := diffLineNumber(line.NewLine)
			gutter := fmt.Sprintf("%*s %*s │ ", oldWidth, oldNumber, newWidth, newNumber)
			marker := " "
			content := sanitizeTerminalText(line.Text)
			switch line.Kind {
			case "add":
				marker = "+"
				content = c.style(content, ansiGreen)
			case "delete":
				marker = "−"
				content = c.style(content, ansiRed)
			default:
				content = c.muted(content)
			}
			if line.NoNewline {
				content += c.muted("  [no newline]")
			}
			fmt.Fprintf(c.out, "%s %s%s\n", c.style(marker, mapDiffMarkerStyle(line.Kind)), c.muted(gutter), content)
		}
	}
	if change.Truncated {
		fmt.Fprintln(c.out, c.muted("… diff preview limited"))
	}
}

func (c *Console) PrintProcessResult(result processResultMeta) {
	marker := "×"
	style := ansiRed
	if processRunning(result.Status) {
		marker = "◌"
		style = ansiGray
	} else if processSucceeded(result.Status) {
		marker = "✓"
		style = ansiGreen
	}
	fmt.Fprintf(c.out, "%s %s\n", c.style(marker, style), c.muted(processStatusText(result)))
	if !processSucceeded(result.Status) && !processRunning(result.Status) {
		for _, line := range processFailureTailLines(result.FailureTail, 6) {
			fmt.Fprintf(c.out, "  %s\n", c.style(sanitizeTerminalText(line), ansiRed))
		}
	}
}

func diffNumberWidths(change fileChangeMeta) (int, int) {
	oldWidth, newWidth := 1, 1
	for _, hunk := range change.Hunks {
		for _, line := range hunk.Lines {
			oldWidth = max(oldWidth, len(diffLineNumber(line.OldLine)))
			newWidth = max(newWidth, len(diffLineNumber(line.NewLine)))
		}
	}
	return oldWidth, newWidth
}

func mapDiffMarkerStyle(kind string) string {
	if kind == "add" {
		return ansiGreen
	}
	if kind == "delete" {
		return ansiRed
	}
	return ansiGray
}

func (c *Console) muted(text string) string {
	return c.style(text, ansiGray)
}

func (c *Console) style(text, style string) string {
	if !c.useStyle || text == "" {
		return text
	}
	return style + text + ansiReset
}

func (c *Console) renderMarkdownLine(line string) string {
	if !c.useStyle {
		return line
	}
	newline := ""
	if strings.HasSuffix(line, "\n") {
		line = strings.TrimSuffix(line, "\n")
		newline = "\n"
	}

	base := ""
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	if level, text, ok := parseHeading(trimmed); ok {
		line = indent + text
		base = ansiBold + ansiCyan
		if level >= 3 {
			base = ansiCyan
		}
	}

	return base + renderInlineMarkdownMarkers(line, base) + ansiReset + newline
}

func (c *Console) renderTable(lines []string) []string {
	rows := make([][]string, 0, len(lines)-1)
	for i, line := range lines {
		if i == 1 {
			continue
		}
		rows = append(rows, splitTableCells(line))
	}
	if len(rows) == 0 {
		return nil
	}
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for col, cell := range row {
			if n := c.markdownCellWidth(cell); n > widths[col] {
				widths[col] = n
			}
		}
	}

	out := make([]string, 0, len(rows)+1)
	for i, row := range rows {
		out = append(out, c.renderTableRow(row, widths, i == 0))
		if i == 0 {
			out = append(out, c.muted(renderTableSeparator(widths)))
		}
	}
	return out
}

func (c *Console) renderTableRow(row []string, widths []int, header bool) string {
	cells := make([]string, len(widths))
	for i := range widths {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		if c.useStyle {
			cell = renderInlineMarkdownMarkers(cell, "")
		}
		if pad := widths[i] - visibleLen(cell); pad > 0 {
			cell += strings.Repeat(" ", pad)
		}
		if header {
			cell = c.style(cell, ansiBold+ansiCyan)
		}
		cells[i] = cell
	}
	return c.muted("|") + " " + strings.Join(cells, " "+c.muted("|")+" ") + " " + c.muted("|")
}

func (c *Console) markdownCellWidth(cell string) int {
	if !c.useStyle {
		return visibleLen(cell)
	}
	return visibleLen(renderInlineMarkdownMarkers(cell, ""))
}

func renderTableSeparator(widths []int) string {
	parts := make([]string, len(widths))
	for i, width := range widths {
		if width < 3 {
			width = 3
		}
		parts[i] = strings.Repeat("-", width)
	}
	return "| " + strings.Join(parts, " | ") + " |"
}

func isTableSeparatorLine(line string) bool {
	cells := splitTableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		if strings.Trim(cell, "-:") != "" || !strings.Contains(cell, "-") {
			return false
		}
	}
	return true
}

func splitTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func parseHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level+1:]), true
}

func renderInlineMarkdownMarkers(text, base string) string {
	var b strings.Builder
	bold := false
	code := false
	for i := 0; i < len(text); {
		if !code && strings.HasPrefix(text[i:], "**") {
			if bold {
				b.WriteString(ansiReset)
				b.WriteString(base)
			} else {
				b.WriteString(ansiBold)
			}
			bold = !bold
			i += 2
			continue
		}
		if text[i] == '`' {
			if code {
				b.WriteString(ansiReset)
				b.WriteString(base)
				if bold {
					b.WriteString(ansiBold)
				}
			} else {
				b.WriteString(ansiYellow)
			}
			code = !code
			i++
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	if bold || code {
		b.WriteString(ansiReset)
		b.WriteString(base)
	}
	return b.String()
}
