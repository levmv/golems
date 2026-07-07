//go:build linux

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the sandbox re-exec target: when the trampoline re-execs
// the test binary (env CALIBAN_SANDBOX_CMD set), become the sandboxed shell
// child instead of running the test suite.
func TestMain(m *testing.M) {
	if os.Getenv(envSandboxCmd) != "" {
		RunSandboxedShell()
		return
	}
	if os.Getenv(envSandboxRunner) != "" {
		RunSandboxedRunner()
		return
	}
	os.Exit(m.Run())
}

// Under the sandbox the shell may read/write its workspace but cannot read a
// file outside the Landlock allow-list — even though it runs as the same user.
func TestSandboxHidesSecretsAllowsWorkspace(t *testing.T) {
	workdir := t.TempDir()

	// The "secret" stands in for caliban's DB/credentials: place it outside any
	// granted path. The test's cwd is the package dir (under the repo), which the
	// allow-list does not grant, so a file there is unreachable from the sandbox.
	secretDir, err := os.MkdirTemp(".", "caliban-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(secretDir)
	secret, err := filepath.Abs(filepath.Join(secretDir, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := Shell(workdir, 5*time.Second, 8192, SandboxRequire, nil)

	out := runForTest(t, tool, shellArgs{Command: "cat " + secret})
	if strings.Contains(out, "(sandbox:") {
		t.Skipf("Landlock unavailable on this kernel: %q", out)
	}
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("sandbox failed to hide the secret: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "permission denied") {
		t.Fatalf("expected a permission error reading outside the sandbox, got: %q", out)
	}

	// The workspace itself is read-write.
	out = runForTest(t, tool, shellArgs{Command: "echo hi > note.txt && cat note.txt"})
	if strings.TrimSpace(out) != "hi" {
		t.Fatalf("workspace not writable under sandbox: %q", out)
	}
}

func TestSandboxAllowsUserLocalBinReadExecOnly(t *testing.T) {
	workdir := t.TempDir()
	homeDir, err := os.MkdirTemp(".", "caliban-home")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(homeDir)
	home, err := filepath.Abs(homeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(localBin, "agy-test")
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\necho local-bin-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := Shell(workdir, 5*time.Second, 8192, SandboxRequire, nil)

	out := runForTest(t, tool, shellArgs{Command: toolPath})
	if strings.Contains(out, "(sandbox:") {
		t.Skipf("Landlock unavailable on this kernel: %q", out)
	}
	if strings.TrimSpace(out) != "local-bin-ok" {
		t.Fatalf("local bin executable was not allowed: %q", out)
	}

	out = runForTest(t, tool, shellArgs{Command: "echo changed > " + toolPath})
	if strings.Contains(out, "changed") {
		t.Fatalf("unexpected stdout from failed write: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "permission denied") {
		t.Fatalf("expected local bin to be read/exec only, got: %q", out)
	}
}

func TestRunnerSandboxAllowsStateButKeepsWorkspaceReadOnly(t *testing.T) {
	workBase, err := os.MkdirTemp(".", "caliban-runner-work")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workBase)
	workdir, err := filepath.Abs(workBase)
	if err != nil {
		t.Fatal(err)
	}
	homeBase, err := os.MkdirTemp(".", "caliban-runner-home")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(homeBase)
	home, err := filepath.Abs(homeBase)
	if err != nil {
		t.Fatal(err)
	}
	localBin := filepath.Join(home, ".local", "bin")
	stateDir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(localBin, "agy")
	script := "#!/bin/sh\n" +
		"echo runner-ok\n" +
		"echo state-ok > \"$HOME/.gemini/antigravity-cli/state.txt\" || echo state-denied\n" +
		"echo workspace-write > \"$PWD/workspace.txt\" || echo workspace-denied\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &RunnerManager{
		workdir:   workdir,
		home:      home,
		sandbox:   SandboxRequire,
		maxOutput: 8192,
		profiles: map[string]runnerProfile{
			"agy": {
				Name:        "agy",
				Description: "test agy",
				Executable:  exe,
				Available:   true,
				StateDirs:   []string{stateDir},
				ExecDirs:    []string{localBin},
			},
		},
	}
	out, err := manager.Run(context.Background(), runnerRunArgs{
		Runner:         "agy",
		Prompt:         "test",
		TimeoutSeconds: 2,
	})
	if err != nil {
		t.Fatalf("runner run: %v", err)
	}
	if strings.Contains(out, "(runner sandbox:") {
		t.Skipf("Landlock unavailable on this kernel: %q", out)
	}
	if !strings.Contains(out, "runner-ok") {
		t.Fatalf("runner executable did not run: %q", out)
	}
	if !strings.Contains(out, "workspace-denied") {
		t.Fatalf("workspace write should be denied in read_only mode: %q", out)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state.txt")); err != nil {
		t.Fatalf("runner state write should be allowed: %v\noutput: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(workdir, "workspace.txt")); err == nil {
		t.Fatalf("workspace file was unexpectedly created")
	}
}
