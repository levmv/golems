package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructionsUsesCurrentHierarchy(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(original) }()

	root := t.TempDir()
	nested := filepath.Join(root, "pkg", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "AGENTS.md"), []byte("token=known-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	prompts, err := loadInstructions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[0], "root rules") || !strings.Contains(prompts[1], "pkg/AGENTS.md") || !strings.Contains(prompts[1], "known-secret") {
		t.Fatalf("prompts = %#v", prompts)
	}

	if err := os.WriteFile(filepath.Join(root, "pkg", "AGENTS.md"), []byte("current rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompts, err = loadInstructions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "current rules") {
		t.Fatalf("instructions were not reloaded: %#v", prompts)
	}
}

func TestLoadInstructionsRejectsSymlinkOutsideWorkspace(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(original) }()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInstructions(root); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("load error = %v", err)
	}

	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "AGENTS.md"), []byte("outside nested"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "nested")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, _, err := readInstruction(root, "nested/AGENTS.md"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("nested load error = %v", err)
	}
}
