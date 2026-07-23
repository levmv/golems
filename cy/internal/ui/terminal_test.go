package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestSanitizeTerminalTextRemovesCSIAndStringControls(t *testing.T) {
	input := "safe\x1b[31mred\x1b[0m " +
		"\x1b]52;c;Y2xpcGJvYXJk\aafter " +
		"\x1bP1;2|device-control\x1b\\done\b\r"
	want := "safered after done"
	if got := sanitizeTerminalText(input); got != want {
		t.Fatalf("sanitizeTerminalText() = %q, want %q", got, want)
	}
}

func TestConsoleNeverWritesUntrustedEscapeSequences(t *testing.T) {
	var out bytes.Buffer
	console := &Console{out: &out, useStyle: false}
	console.PrintMarkdown("hello \x1b]52;c;c2VjcmV0\aworld")
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "52;c") {
		t.Fatalf("console output contains terminal control sequence: %q", out.String())
	}
}
