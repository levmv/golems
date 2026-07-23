package ui

import (
	"strings"
	"testing"
)

func TestFormatChangedFilesGroupsUniquePaths(t *testing.T) {
	paths := []string{"cy/main.go", "cy/screen.go", "pkg/golem/agent.go"}
	paths = appendUniquePath(paths, "cy/main.go")
	got := formatChangedFiles(paths)
	want := "changed 3 files · cy/ → main.go, screen.go · pkg/golem/agent.go"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestConsolePrintsOneChangeSummaryPerFile(t *testing.T) {
	var out strings.Builder
	console := NewConsole(&out)
	change := buildFileChangeMeta("cy/main.go", "edited", []byte("old\n"), []byte("new\n"))
	console.recordFileChange(change)
	console.recordFileChange(change)
	console.PrintChangeSummary()
	if got := out.String(); !strings.Contains(got, "changed 1 file · cy/main.go") {
		t.Fatalf("summary output = %q", got)
	}
}
