package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunnerRunBuildsSemanticPiInvocation(t *testing.T) {
	workdir := t.TempDir()
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "pi")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho HOME=$HOME\necho PWD=$PWD\necho ARGS=\"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &RunnerManager{
		workdir:   workdir,
		home:      home,
		sandbox:   SandboxOff,
		maxOutput: 4096,
		profiles: map[string]runnerProfile{
			"pi": {
				Name:        "pi",
				Description: "test pi",
				Executable:  exe,
				Available:   true,
			},
		},
	}

	out, err := manager.Run(context.Background(), runnerRunArgs{
		Runner:         "pi",
		Prompt:         "inspect this",
		Model:          "deepseek-v4-flash",
		Session:        "continue",
		TimeoutSeconds: 2,
	})
	if err != nil {
		t.Fatalf("runner run: %v", err)
	}
	for _, want := range []string{
		"HOME=" + home,
		"PWD=" + workdir,
		"--print",
		"--approve",
		"--continue",
		"--model deepseek-v4-flash",
		"--tools read,grep,find,ls",
		"trusted external runner",
		"inspect this",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in runner output:\n%s", want, out)
		}
	}
}

func TestRunnerModelsUnsupported(t *testing.T) {
	manager := &RunnerManager{
		workdir: t.TempDir(),
		home:    t.TempDir(),
		sandbox: SandboxOff,
		profiles: map[string]runnerProfile{
			"codex": {
				Name:        "codex",
				Description: "test codex",
				Executable:  "/bin/true",
				Available:   true,
			},
		},
	}
	out, err := manager.RunModels(context.Background(), "codex", 0)
	if err != nil {
		t.Fatalf("runner models: %v", err)
	}
	if !strings.Contains(out, "does not expose") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestRunnerBuildsCurrentCodexInvocations(t *testing.T) {
	manager := &RunnerManager{workdir: "/work"}
	profile := runnerProfile{Name: "codex"}

	argv, err := manager.buildRunArgs(profile, runnerRunArgs{
		Prompt:          "review",
		WorkspaceAccess: runnerWorkspaceReadOnly,
		Model:           "gpt-5",
	}, "review", false, "")
	if err != nil {
		t.Fatalf("build new codex args: %v", err)
	}
	got := strings.Join(argv, " ")
	for _, want := range []string{"-a never", "exec", "--skip-git-repo-check", "-C /work", "-s read-only", "-m gpt-5", "review"} {
		if !strings.Contains(got, want) {
			t.Fatalf("new codex args missing %q: %v", want, argv)
		}
	}
	if strings.Contains(got, "dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("new codex args include full bypass flag: %v", argv)
	}

	argv, err = manager.buildRunArgs(profile, runnerRunArgs{
		Prompt:  "continue",
		Session: "id:1234",
		Model:   "gpt-5",
	}, "continue", false, "/tmp/codex-last.txt")
	if err != nil {
		t.Fatalf("build resume codex args: %v", err)
	}
	want := []string{"-a", "never", "exec", "resume", "--skip-git-repo-check", "-m", "gpt-5", "-o", "/tmp/codex-last.txt", "1234", "continue"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected resume args:\n got %v\nwant %v", argv, want)
	}
}

func TestRunnerBuildsLowFrictionButBoundedInvocations(t *testing.T) {
	manager := &RunnerManager{workdir: "/work"}

	argv, err := manager.buildRunArgs(runnerProfile{Name: "agy"}, runnerRunArgs{Prompt: "inspect"}, "inspect", false, "")
	if err != nil {
		t.Fatalf("build agy args: %v", err)
	}
	got := strings.Join(argv, " ")
	if !strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("agy args should lower permission prompts: %v", argv)
	}
	if len(argv) < 2 || argv[len(argv)-2] != "--print" || argv[len(argv)-1] != "inspect" {
		t.Fatalf("agy --print must take the prompt as its value: %v", argv)
	}

	argv, err = manager.buildRunArgs(runnerProfile{Name: "claude"}, runnerRunArgs{Prompt: "edit"}, "edit", true, "")
	if err != nil {
		t.Fatalf("build claude args: %v", err)
	}
	got = strings.Join(argv, " ")
	for _, want := range []string{"--allowedTools=Read,Grep,Glob,LS,Edit,Write,Bash", "--permission-mode acceptEdits"} {
		if !strings.Contains(got, want) {
			t.Fatalf("claude write args missing %q: %v", want, argv)
		}
	}
	if strings.Contains(got, "--dangerously-skip-permissions") || strings.Contains(got, "bypassPermissions") {
		t.Fatalf("claude args should not use full permission bypass by default: %v", argv)
	}
}

func TestAgyProfileAllowsGeminiConfigRootReadOnly(t *testing.T) {
	home := t.TempDir()
	profiles := builtinRunnerProfiles(home)
	var agy runnerProfile
	for _, profile := range profiles {
		if profile.Name == "agy" {
			agy = profile
			break
		}
	}
	if agy.Name == "" {
		t.Fatal("agy profile not found")
	}
	want := filepath.Join(home, ".gemini")
	if !slices.Contains(agy.ExtraRODirs, want) {
		t.Fatalf("agy profile should allow read-only %s, got %v", want, agy.ExtraRODirs)
	}
}

func TestAgyCommandSpecAllowsWorkspaceGeminiState(t *testing.T) {
	workdir := t.TempDir()
	if err := os.Mkdir(filepath.Join(workdir, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := &RunnerManager{workdir: workdir, home: t.TempDir(), sandbox: SandboxAuto}
	spec := manager.commandSpec(runnerProfile{Name: "agy"}, []string{"--print", "hello"}, false)
	want := filepath.Join(workdir, ".gemini")
	if !slices.Contains(spec.RWDirs, want) {
		t.Fatalf("agy spec should allow workspace-local gemini state %s, got %v", want, spec.RWDirs)
	}
}

func TestSanitizeRunnerOutputRemovesAgyTcmallocNoise(t *testing.T) {
	out := sanitizeRunnerOutput("agy", "1020797 third_party/tcmalloc/parameters.cc:583] Using per-thread caches requires linking against :tcmalloc_deprecated_perthread.\nTEST123\n")
	if out != "TEST123" {
		t.Fatalf("unexpected sanitized output: %q", out)
	}
	other := sanitizeRunnerOutput("pi", "1020797 third_party/tcmalloc/parameters.cc:583] Using per-thread caches requires linking against :tcmalloc_deprecated_perthread.\nTEST123\n")
	if !strings.Contains(other, "tcmalloc") {
		t.Fatalf("non-agy output should not be filtered: %q", other)
	}
}
