package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
)

func TestConsoleMarkdownTable(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: false}

	console.PrintMarkdown("| Name | Value |\n| --- | ---: |\n| a | 10 |")

	got := out.String()
	if strings.Contains(got, "---:") {
		t.Fatalf("table separator leaked into rendered table: %q", got)
	}
	if !strings.Contains(got, "| Name") || !strings.Contains(got, "| a") {
		t.Fatalf("table content missing: %q", got)
	}
}

func TestTUIWorkingIndicatorAppearsBetweenTranscriptAndEditor(t *testing.T) {
	var out bytes.Buffer
	model := cyTUIModel{
		console:       &Console{out: &out, useStyle: false},
		agent:         screenAgentStub{},
		spinner:       spinner.New(spinner.WithSpinner(spinner.Line)),
		blocks:        []screenBlock{{kind: screenBlockAssistant, text: "hello"}},
		width:         80,
		working:       true,
		turnStartedAt: time.Now().Add(-83 * time.Second),
	}

	lines := model.renderTranscriptLines()
	if !strings.Contains(strings.Join(lines, "\n"), "hello") {
		t.Fatalf("transcript lines missed assistant text: %q", lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "Working") {
		t.Fatalf("working indicator leaked into transcript: %q", lines)
	}
	if working := model.workingIndicatorLine(); !strings.Contains(working, "Working (1m 23s • esc to interrupt)") {
		t.Fatalf("working line = %q, want elapsed working indicator", working)
	}
	if footer := model.footerMetaLine(); strings.Contains(footer, "Working") {
		t.Fatalf("working indicator remained in footer: %q", footer)
	}
}

func TestFormatTurnDuration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{duration: 900 * time.Millisecond, want: "0s"},
		{duration: 23 * time.Second, want: "23s"},
		{duration: 83 * time.Second, want: "1m 23s"},
		{duration: 3723 * time.Second, want: "1h 2m 3s"},
	} {
		if got := formatTurnDuration(test.duration); got != test.want {
			t.Fatalf("formatTurnDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestTruncateANSIPreservesVisibleWidth(t *testing.T) {
	got := truncateANSI(ansiCyan+"abcdef"+ansiReset, 3)
	if visibleLen(got) != 3 {
		t.Fatalf("visibleLen(truncated) = %d, want 3; got %q", visibleLen(got), got)
	}
	if !strings.Contains(got, ansiReset) {
		t.Fatalf("truncated ANSI string missed reset: %q", got)
	}
}

func TestTerminalWidthAccountsForWideCharacters(t *testing.T) {
	styled := ansiCyan + "界🙂x" + ansiReset
	if got := visibleLen(styled); got != 5 {
		t.Fatalf("visibleLen() = %d, want 5", got)
	}
	truncated := truncateANSI(styled, 4)
	if got := visibleLen(truncated); got != 4 || strings.Contains(truncated, "x") {
		t.Fatalf("truncateANSI() width=%d value=%q", got, truncated)
	}
	middle := truncateMiddle("ab界🙂xyz", 6)
	if got := visibleLen(middle); got > 6 || !strings.Contains(middle, "…") {
		t.Fatalf("truncateMiddle() = %q, width %d", middle, got)
	}
}

func TestConsoleMarkdownPlainKeepsOriginalText(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: false}

	console.PrintMarkdown("## Title\nhello **world**")

	want := "## Title\nhello **world**"
	if out.String() != want {
		t.Fatalf("markdown = %q, want %q", out.String(), want)
	}
}

func TestConsoleMarkdownStyledRendersHeadingsAndBold(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: true}

	console.PrintMarkdown("## Title\nhello **world**")

	got := out.String()
	if strings.Contains(got, "## Title") {
		t.Fatalf("styled markdown kept heading marker: %q", got)
	}
	if !strings.Contains(got, "Title") || !strings.Contains(got, "world") {
		t.Fatalf("styled markdown missed content: %q", got)
	}
	if !strings.Contains(got, ansiBold) || !strings.Contains(got, ansiCyan) {
		t.Fatalf("styled markdown missed ANSI styling: %q", got)
	}
}

func TestConsoleMarkdownStyledRendersInlineCode(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: true}

	console.PrintMarkdown("file `README.md`")

	got := out.String()
	if strings.Contains(got, "`README.md`") {
		t.Fatalf("styled markdown kept inline code markers: %q", got)
	}
	if !strings.Contains(got, "README.md") || !strings.Contains(got, ansiYellow) {
		t.Fatalf("styled markdown missed inline code styling: %q", got)
	}
}

func TestConsoleMarkdownRemovesFencedCodeLanguage(t *testing.T) {
	for _, styled := range []bool{false, true} {
		var out bytes.Buffer
		console := &Console{out: &out, useStyle: styled}
		console.PrintMarkdown("```json\n{\"file_count\": 2}\n```")

		got := out.String()
		if strings.Contains(got, "json") || strings.Contains(got, "```") {
			t.Fatalf("styled=%v fence marker leaked: %q", styled, got)
		}
		if !strings.Contains(got, `{"file_count": 2}`) {
			t.Fatalf("styled=%v code content missing: %q", styled, got)
		}
	}
}

func TestConsoleMarkdownTableRendersInlineCode(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: true}

	console.PrintMarkdown("| File |\n| --- |\n| `README.md` |")

	got := out.String()
	if strings.Contains(got, "`README.md`") {
		t.Fatalf("styled table kept inline code markers: %q", got)
	}
	if !strings.Contains(got, "README.md") || !strings.Contains(got, ansiYellow) {
		t.Fatalf("styled table missed inline code styling: %q", got)
	}
}
