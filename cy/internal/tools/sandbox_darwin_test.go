//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeatbeltRestrictsFilesystem(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(userHome, ".cy-seatbelt-workspace-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	workdir := filepath.Join(root, "work")
	toolHome, err := os.MkdirTemp(userHome, ".cy-seatbelt-tool-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(toolHome) })
	outsideDir, err := os.MkdirTemp(userHome, ".cy-seatbelt-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outsideDir) })
	outside := filepath.Join(outsideDir, "outside.txt")
	for _, dir := range []string{workdir, toolHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := "printf ok > inside.txt; " +
		"if cat " + shellQuoteForTest(outside) + " >/dev/null 2>&1; then exit 41; fi; " +
		"if printf nope > " + shellQuoteForTest(outside) + " 2>/dev/null; then exit 42; fi"
	cmd, err := sandboxedBashCommand(command, root, workdir, toolHome, sandboxOn)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seatbelt command failed: %v: %s", err, output)
	}
	if raw, err := os.ReadFile(filepath.Join(workdir, "inside.txt")); err != nil || strings.TrimSpace(string(raw)) != "ok" {
		t.Fatalf("workspace write = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "secret" {
		t.Fatalf("outside file changed: %q, %v", raw, err)
	}
}

func TestSeatbeltProfileKeepsNetworkOpen(t *testing.T) {
	if !strings.Contains(seatbeltProfile, "(allow network*)") {
		t.Fatal("seatbelt profile does not keep network open")
	}
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
