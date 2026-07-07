package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func runForTest(t *testing.T, tool golem.Tool, args shellArgs) string {
	t.Helper()
	return runToolForTest(t, tool, args)
}

func runToolForTest(t *testing.T, tool golem.Tool, args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Run(context.Background(), llm.ToolCall{
		Function: llm.ToolFunction{Name: tool.Definition.Function.Name, Arguments: string(raw)},
	})
	if err != nil {
		t.Fatalf("%s tool error: %v", tool.Definition.Function.Name, err)
	}
	return res.Content
}

func backgroundManagerForTest(t *testing.T) (*BackgroundManager, string) {
	t.Helper()
	dataDir := t.TempDir()
	workdir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "caliban.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	manager, err := NewBackgroundManager(st.DB(), workdir, filepath.Join(dataDir, "background-tasks"), 4096, SandboxOff)
	if err != nil {
		t.Fatalf("new background manager: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.StopAll(ctx, "test cleanup")
		st.Close()
	})
	return manager, workdir
}

func taskIDFromOutput(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^task_id: (\S+)$`).FindStringSubmatch(out)
	if len(m) != 2 {
		t.Fatalf("task_id not found in output:\n%s", out)
	}
	return m[1]
}

func TestShellEchoRoundTrip(t *testing.T) {
	tool := Shell(t.TempDir(), 5*time.Second, 4096, SandboxOff, nil)
	out := runForTest(t, tool, shellArgs{Command: "echo hello world"})
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestShellExitCodeReported(t *testing.T) {
	tool := Shell(t.TempDir(), 5*time.Second, 4096, SandboxOff, nil)
	out := runForTest(t, tool, shellArgs{Command: "echo oops; exit 3"})
	if !strings.Contains(out, "oops") {
		t.Fatalf("missing stdout: %q", out)
	}
	if !strings.Contains(out, "(exit status 3)") {
		t.Fatalf("missing exit status: %q", out)
	}
}

func TestShellStderrCaptured(t *testing.T) {
	tool := Shell(t.TempDir(), 5*time.Second, 4096, SandboxOff, nil)
	out := runForTest(t, tool, shellArgs{Command: "echo to-stderr 1>&2"})
	if !strings.Contains(out, "to-stderr") {
		t.Fatalf("stderr not captured: %q", out)
	}
}

func TestShellTimeoutKills(t *testing.T) {
	tool := Shell(t.TempDir(), time.Hour, 4096, SandboxOff, nil)
	start := time.Now()
	out := runForTest(t, tool, shellArgs{Command: "sleep 30; echo done", TimeoutSeconds: 1})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout did not kill promptly: took %s", elapsed)
	}
	if strings.Contains(out, "done") {
		t.Fatalf("command should have been killed before printing: %q", out)
	}
	if !strings.Contains(out, "killed: timeout") {
		t.Fatalf("missing timeout marker: %q", out)
	}
}

func TestShellRunInBackgroundStartsTask(t *testing.T) {
	manager, workdir := backgroundManagerForTest(t)
	tool := Shell(workdir, time.Hour, 4096, SandboxOff, manager)

	out := runForTest(t, tool, shellArgs{
		Command:         "printf hello; sleep 0.1; printf done",
		RunInBackground: true,
	})
	if !strings.Contains(out, "background task started") {
		t.Fatalf("expected background start result, got:\n%s", out)
	}
	taskID := taskIDFromOutput(t, out)

	got, ok, err := manager.ReadOutput(context.Background(), taskID, nil, 4096, true, 2*time.Second)
	if err != nil {
		t.Fatalf("read task output: %v", err)
	}
	if !ok {
		t.Fatalf("task %s not found", taskID)
	}
	if got.Task.Status != BackgroundStatusCompleted {
		t.Fatalf("expected completed task, got %+v", got.Task)
	}
	if got.Content != "hellodone" {
		t.Fatalf("unexpected task output: %q", got.Content)
	}
}

func TestBackgroundTaskOutputReadsIncrementally(t *testing.T) {
	manager, _ := backgroundManagerForTest(t)
	task, err := manager.StartShell(WithRunInfo(context.Background(), 2, 42), "printf alpha", 0)
	if err != nil {
		t.Fatalf("start background task: %v", err)
	}
	if task.ConversationID != 2 || task.StartedByRunID != 42 {
		t.Fatalf("run ownership not recorded: %+v", task)
	}
	var rawStartedAt int64
	if err := manager.db.QueryRowContext(context.Background(),
		`SELECT started_at FROM background_tasks WHERE id = ?`, task.ID).Scan(&rawStartedAt); err != nil {
		t.Fatalf("read raw started_at: %v", err)
	}
	if rawStartedAt < 1_000_000_000_000 {
		t.Fatalf("started_at stored as seconds, got %d", rawStartedAt)
	}

	first, ok, err := manager.ReadOutput(context.Background(), task.ID, nil, 4096, true, 2*time.Second)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !ok {
		t.Fatalf("task %s not found", task.ID)
	}
	if first.Content != "alpha" {
		t.Fatalf("unexpected first content: %q", first.Content)
	}
	if first.NextOffset != 5 {
		t.Fatalf("unexpected first next offset: %d", first.NextOffset)
	}

	second, ok, err := manager.ReadOutput(context.Background(), task.ID, nil, 4096, false, 0)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !ok {
		t.Fatalf("task %s not found on second read", task.ID)
	}
	if second.Content != "" || second.Offset != first.NextOffset {
		t.Fatalf("expected no new output from remembered offset, got content=%q offset=%d", second.Content, second.Offset)
	}
}

func TestBackgroundTaskStopKillsProcess(t *testing.T) {
	manager, _ := backgroundManagerForTest(t)
	task, err := manager.StartShell(context.Background(), "printf started; sleep 30; printf done", 0)
	if err != nil {
		t.Fatalf("start background task: %v", err)
	}

	stopped, ok, err := manager.Stop(context.Background(), task.ID, "test stop")
	if err != nil {
		t.Fatalf("stop task: %v", err)
	}
	if !ok {
		t.Fatalf("task %s not found", task.ID)
	}
	if stopped.Status != BackgroundStatusKilled {
		t.Fatalf("expected killed task, got %+v", stopped)
	}
	if stopped.StopReason != "test stop" {
		t.Fatalf("stop reason not recorded: %+v", stopped)
	}

	zero := int64(0)
	out, ok, err := manager.ReadOutput(context.Background(), task.ID, &zero, 4096, false, 0)
	if err != nil {
		t.Fatalf("read stopped task output: %v", err)
	}
	if !ok {
		t.Fatalf("task %s not found after stop", task.ID)
	}
	if strings.Contains(out.Content, "done") {
		t.Fatalf("command continued after stop: %q", out.Content)
	}
}

func TestShellRejectsTrailingAmpersand(t *testing.T) {
	tool := Shell(t.TempDir(), 5*time.Second, 4096, SandboxOff, nil)
	out := runForTest(t, tool, shellArgs{Command: "sleep 1 &"})
	if !strings.Contains(out, "shell refused") || !strings.Contains(out, "run_in_background") {
		t.Fatalf("expected managed-background guidance, got: %q", out)
	}
}

func TestShellEnvScrubbed(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "should-not-leak")
	workdir := t.TempDir()
	tool := Shell(workdir, 5*time.Second, 4096, SandboxOff, nil)
	out := runForTest(t, tool, shellArgs{Command: "env"})

	if strings.Contains(out, "SECRET_TOKEN") || strings.Contains(out, "should-not-leak") {
		t.Fatalf("secret leaked into shell env: %q", out)
	}
	if !strings.Contains(out, "HOME="+workdir) {
		t.Fatalf("HOME not pinned to workdir: %q", out)
	}
	// Every printed VAR= line must be in the allow-list (plus HOME and the
	// variables bash sets for itself: PWD, SHLVL, _).
	allowed := map[string]bool{"HOME": true, "PWD": true, "SHLVL": true, "_": true}
	for _, k := range shellEnvAllowlist {
		allowed[k] = true
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue // continuation line of a multiline value
		}
		if !allowed[key] {
			t.Fatalf("unexpected env var %q in shell env:\n%s", key, out)
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	// 90 bytes of distinct head/tail content, capped at 30.
	b := []byte(strings.Repeat("A", 45) + strings.Repeat("B", 45))
	out := truncateMiddle(b, 30)
	if len(out) <= 30 {
		t.Fatalf("expected marker to inflate length, got %d", len(out))
	}
	if !strings.HasPrefix(out, "AAAA") || !strings.HasSuffix(out, "BBBB") {
		t.Fatalf("head/tail not preserved: %q", out)
	}
	if !strings.Contains(out, "bytes truncated") {
		t.Fatalf("missing truncation marker: %q", out)
	}
	// Under the cap: returned verbatim.
	if got := truncateMiddle([]byte("short"), 30); got != "short" {
		t.Fatalf("short output altered: %q", got)
	}
}
