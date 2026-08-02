package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/cy/internal/session"
)

func TestParseInvocationResume(t *testing.T) {
	invocation := parseInvocation([]string{"resume", "01234567", "continue", "now"}, false)
	if !invocation.Resume || invocation.SessionID != "01234567" || strings.Join(invocation.Args, " ") != "continue now" {
		t.Fatalf("invocation = %#v", invocation)
	}
}

func TestParseInvocationBareResume(t *testing.T) {
	invocation := parseInvocation([]string{"resume"}, false)
	if !invocation.Resume || invocation.SessionID != "" || len(invocation.Args) != 0 {
		t.Fatalf("invocation = %#v", invocation)
	}
}

func TestParseInvocationExplicitPromptSkipsResume(t *testing.T) {
	args := []string{"resume", "the", "discussion"}
	invocation := parseInvocation(args, true)
	if invocation.Resume || strings.Join(invocation.Args, " ") != strings.Join(args, " ") {
		t.Fatalf("invocation = %#v", invocation)
	}
}

func TestHasFlagTerminator(t *testing.T) {
	flags := flag.NewFlagSet("cy", flag.ContinueOnError)
	flags.String("root", ".", "root")
	flags.Bool("v", false, "verbose")

	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"--root", ".", "--", "resume"}, want: true},
		{args: []string{"-v", "--", "resume"}, want: true},
		{args: []string{"resume"}, want: false},
	} {
		if got := hasFlagTerminator(flags, test.args); got != test.want {
			t.Fatalf("hasFlagTerminator(%q) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestLatestSessionIDReturnsNewest(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	createSessionAt(t, home, workspace, time.Unix(100, 0))
	newerID := createSessionAt(t, home, workspace, time.Unix(200, 0))

	got, err := latestSessionID(home, workspace)
	if err != nil || got != newerID {
		t.Fatalf("latestSessionID() = %q, %v; want %q", got, err, newerID)
	}
}

func createSessionAt(t *testing.T, home, workspace string, updatedAt time.Time) string {
	t.Helper()
	journal, err := session.Create(session.CreateOptions{Home: home, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	id := journal.ID()
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "sessions", id, "events.jsonl")
	if err := os.Chtimes(path, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLatestSessionIDReportsEmptyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	_, err := latestSessionID(t.TempDir(), workspace)
	if err == nil || !strings.Contains(err.Error(), "no resumable sessions") || !strings.Contains(err.Error(), workspace) {
		t.Fatalf("latestSessionID() error = %v", err)
	}
}
