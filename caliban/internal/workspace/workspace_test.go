package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "workspace")
	w, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if info, err := os.Stat(w.Root()); err != nil || !info.IsDir() {
		t.Fatalf("root not created as dir: err=%v", err)
	}
	if !filepath.IsAbs(w.Root()) {
		t.Fatalf("Root() not absolute: %q", w.Root())
	}
}

func TestPersonaAndMemoryIndex(t *testing.T) {
	root := t.TempDir()
	w, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Absent files read as empty, not an error.
	for _, tc := range []struct {
		name string
		read func() (string, error)
	}{
		{"Persona", w.Persona},
		{"MemoryIndex", w.MemoryIndex},
	} {
		got, err := tc.read()
		if err != nil {
			t.Fatalf("%s on empty workspace: %v", tc.name, err)
		}
		if got != "" {
			t.Fatalf("%s expected empty, got %q", tc.name, got)
		}
	}

	if err := os.WriteFile(filepath.Join(root, personaFile), []byte("I am Caliban."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, memoryIndexFile), []byte("- fact one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := w.Persona(); err != nil || got != "I am Caliban." {
		t.Fatalf("Persona: got %q err %v", got, err)
	}
	if got, err := w.MemoryIndex(); err != nil || got != "- fact one\n" {
		t.Fatalf("MemoryIndex: got %q err %v", got, err)
	}
}

func TestUpsertMemoryCreatesAndUpdatesFact(t *testing.T) {
	root := t.TempDir()
	w, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	first, err := w.UpsertMemory(
		"User working on Caliban",
		"User is actively working on Caliban and wants the web UI to be minimally usable.",
		"User works on Caliban web UI",
	)
	if err != nil {
		t.Fatalf("UpsertMemory first: %v", err)
	}
	if !first.Created || !first.IndexUpdated {
		t.Fatalf("unexpected first result: %+v", first)
	}
	if !strings.HasPrefix(first.Path, "memory/user-working-on-caliban-") || !strings.HasSuffix(first.Path, ".md") {
		t.Fatalf("unexpected memory path: %q", first.Path)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(first.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, "# User working on Caliban\n\nUser is actively working on Caliban") {
		t.Fatalf("unexpected memory body: %q", got)
	}
	index, err := w.MemoryIndex()
	if err != nil {
		t.Fatal(err)
	}
	if want := "- [User working on Caliban](" + first.Path + ") - User works on Caliban web UI\n"; index != want {
		t.Fatalf("unexpected index:\nwant %q\ngot  %q", want, index)
	}

	second, err := w.UpsertMemory(
		"User working on Caliban",
		"User is actively working on Caliban. PWA polish is important.",
		"",
	)
	if err != nil {
		t.Fatalf("UpsertMemory second: %v", err)
	}
	if second.Created || !second.IndexUpdated || second.Path != first.Path {
		t.Fatalf("unexpected second result: %+v", second)
	}
	index, err = w.MemoryIndex()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(index, first.Path) != 1 {
		t.Fatalf("expected one index entry, got %q", index)
	}
	if !strings.Contains(index, "PWA polish is important") {
		t.Fatalf("expected updated summary, got %q", index)
	}
}

func TestOpenRequiresRoot(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected error for empty root")
	}
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func TestOpenInitsGitRepoAndCommits(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	root := t.TempDir()
	w, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !w.gitEnabled {
		t.Fatal("git should be enabled")
	}
	// Repo initialized with the scaffold committed.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	for _, d := range scaffoldDirs {
		if _, err := os.Stat(filepath.Join(root, d, ".gitkeep")); err != nil {
			t.Fatalf("scaffold %s missing: %v", d, err)
		}
	}
	if out, _ := w.git("log", "--oneline"); !strings.Contains(out, "init workspace") {
		t.Fatalf("expected init commit, log: %q", out)
	}

	// A clean tree commits nothing.
	if err := w.Commit("noop"); err != nil {
		t.Fatalf("Commit(clean): %v", err)
	}
	before, _ := w.git("rev-list", "--count", "HEAD")

	// An agent edit gets committed.
	if err := os.WriteFile(filepath.Join(root, "PERSONA.md"), []byte("I am Caliban."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit("run 1"); err != nil {
		t.Fatalf("Commit(dirty): %v", err)
	}
	after, _ := w.git("rev-list", "--count", "HEAD")
	if strings.TrimSpace(before) == strings.TrimSpace(after) {
		t.Fatalf("expected a new commit, count stayed %s", strings.TrimSpace(after))
	}
	if out, _ := w.git("log", "-1", "--oneline"); !strings.Contains(out, "run 1") {
		t.Fatalf("expected 'run 1' commit, got %q", out)
	}
}

func TestOpenExistingRepoIsIdempotent(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	root := t.TempDir()
	if _, err := Open(root); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	// Reopening must not fail or clobber.
	w, err := Open(root)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if got, err := w.Persona(); err != nil || got != "" {
		t.Fatalf("Persona after reopen: got %q err %v", got, err)
	}
}
