package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAgyRunnerIntegration(t *testing.T) {
	if os.Getenv("CALIBAN_RUN_AGY_INTEGRATION") != "1" {
		t.Skip("set CALIBAN_RUN_AGY_INTEGRATION=1 to run the real agy CLI")
	}
	workdir := os.Getenv("CALIBAN_AGY_INTEGRATION_WORKDIR")
	if workdir == "" {
		workdir = t.TempDir()
	}
	manager := NewRunnerManager(workdir, SandboxAuto, 32768, nil)
	profile, err := manager.profile("agy")
	if err != nil {
		t.Fatalf("agy profile: %v", err)
	}
	argv, err := manager.buildRunArgs(profile, runnerRunArgs{
		Runner: "agy",
		Model:  "Gemini 3.5 Flash (High)",
		Prompt: "Скажи только TEST123",
	}, guardedRunnerPrompt("Скажи только TEST123"), false, "")
	if err != nil {
		t.Fatalf("build agy args: %v", err)
	}
	spec := manager.commandSpec(profile, argv, false)
	t.Logf("agy executable=%s", profile.Executable)
	t.Logf("agy args=%q", argv)
	t.Logf("agy env=%q", spec.Env)
	t.Logf("agy ro_dirs=%q", spec.RODirs)
	t.Logf("agy rw_dirs=%q", spec.RWDirs)
	out, err := manager.Run(context.Background(), runnerRunArgs{
		Runner:         "agy",
		Model:          "Gemini 3.5 Flash (High)",
		Prompt:         "Скажи только TEST123",
		TimeoutSeconds: 30,
	})
	if err != nil {
		t.Fatalf("agy runner failed: %v", err)
	}
	if !strings.Contains(out, "TEST123") {
		t.Fatalf("unexpected agy output after %s:\n%s", 30*time.Second, out)
	}
}
