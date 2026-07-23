//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxedBashNestedWorkdirCanAccessWorkspace(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "nested")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("workspace data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolHome := t.TempDir()
	cmd, err := sandboxedBashCommand("cat ../source.txt > ../copy.txt", root, workdir, toolHome, sandboxAuto)
	if err != nil {
		t.Fatal(err)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandboxed command failed: %v: %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "workspace data\n" {
		t.Fatalf("copy = %q", data)
	}
}
