package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func TestBashReportsExitAndUsesIsolatedEnvironment(t *testing.T) {
	manager := processManagerForTest(t)
	t.Setenv("CY_TEST_SECRET", "must-not-leak")
	result := runProcessTool(t, manager.bash, bashArgs{Command: "printf 'hello\\n'; printf 'stderr\\n' >&2; env; exit 3"})
	if !strings.Contains(result, "status: failed") || !strings.Contains(result, "exit_code: 3") || !strings.Contains(result, "hello") || !strings.Contains(result, "stderr") {
		t.Fatalf("bash result = %q", result)
	}
	if strings.Contains(result, "CY_TEST_SECRET") || strings.Contains(result, "must-not-leak") {
		t.Fatalf("bash leaked parent environment: %q", result)
	}
	if !strings.Contains(result, "HOME="+manager.toolHome) {
		t.Fatalf("bash HOME is not isolated: %q", result)
	}
}

func TestBashReturnsStructuredFailureStatus(t *testing.T) {
	manager := processManagerForTest(t)
	result := runProcessResult(t, manager.bash, bashArgs{Command: "printf 'first\\nlast\\n'; exit 7"})
	meta, ok := processResultMetaFrom(result.Meta)
	if !ok || meta.Status != jobFailed || meta.ExitCode == nil || *meta.ExitCode != 7 || meta.JobID != "" {
		t.Fatalf("process meta = %#v", result.Meta)
	}
	if strings.Contains(result.Content, "job_id:") || len(manager.jobs) != 0 {
		t.Fatalf("completed foreground command was retained as a job: %q / %#v", result.Content, manager.jobs)
	}
	for _, header := range []string{"pid:", "command:", "cwd:", "started_at:", "duration_ms:", "output_bytes:", "discarded_bytes:"} {
		if strings.Contains(result.Content, header) {
			t.Fatalf("foreground result contains service header %q: %q", header, result.Content)
		}
	}
	if meta.OutputBytes == 0 || !strings.Contains(meta.FailureTail, "last") {
		t.Fatalf("process output metadata = %#v", meta)
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	manager := processManagerForTest(t)
	started := time.Now()
	result := runProcessTool(t, manager.bash, bashArgs{Command: "sleep 30; printf done", TimeoutSeconds: 1})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	if !strings.Contains(result, "status: timed_out") {
		t.Fatalf("timeout result = %q", result)
	}
}

func TestBackgroundJobCanBeReadAndStopped(t *testing.T) {
	manager := processManagerForTest(t)
	started := runProcessTool(t, manager.bash, bashArgs{Command: "printf ready; sleep 30", Background: true})
	id := jobIDFromText(t, started)
	job := manager.get(id)
	deadline := time.Now().Add(3 * time.Second)
	for {
		content, _ := job.log.snapshot(32)
		if strings.Contains(string(content), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background job did not produce initial output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	output := runProcessTool(t, manager.job, jobArgs{Action: "output", JobID: id})
	if !strings.Contains(output, "status: running") || !strings.Contains(output, "ready") {
		t.Fatalf("job output = %q", output)
	}
	stopped := runProcessTool(t, manager.job, jobArgs{Action: "stop", JobID: id})
	if !strings.Contains(stopped, "status: killed") {
		t.Fatalf("job stop = %q", stopped)
	}
}

func TestBashCapsOutputWithoutBlockingProcess(t *testing.T) {
	manager := processManagerForTest(t)
	manager.logLimit = 64
	result := runProcessResult(t, manager.bash, bashArgs{Command: "head -c 1024 /dev/zero | tr '\\0' x"})
	meta, ok := processResultMetaFrom(result.Meta)
	if !ok || meta.DiscardedBytes < 900 || !strings.Contains(result.Content, "truncated: true") {
		t.Fatalf("meta=%#v result=%q", result.Meta, result.Content)
	}
}

func TestBashMarksTruncatedPreviewBeforeLogBufferOverflows(t *testing.T) {
	manager := processManagerForTest(t)
	result := runProcessResult(t, manager.bash, bashArgs{Command: "head -c 40000 /dev/zero | tr '\\0' x"})
	meta, ok := processResultMetaFrom(result.Meta)
	if !ok || meta.DiscardedBytes != 0 || meta.OutputBytes != 40000 {
		t.Fatalf("process meta = %#v", result.Meta)
	}
	if !strings.Contains(result.Content, "truncated: true") {
		t.Fatalf("truncated preview was not marked: %q", result.Content)
	}
}

func TestProcessManagerCloseKillsBackgroundJobs(t *testing.T) {
	manager := processManagerForTest(t)
	started := runProcessTool(t, manager.bash, bashArgs{Command: "sleep 30", Background: true})
	id := jobIDFromText(t, started)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	job := manager.get(id)
	job.mu.Lock()
	status := job.status
	job.mu.Unlock()
	if status != jobKilled {
		t.Fatalf("job status = %q, want killed", status)
	}
}

func TestOneShotBashDoesNotExposeBackground(t *testing.T) {
	manager := processManagerForTest(t)
	manager.allowBackground = false
	rawSchema, err := json.Marshal(manager.Tools()[0].Definition.Function.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawSchema), `"background"`) {
		t.Fatalf("one-shot Bash schema exposes background: %s", rawSchema)
	}
	rawArgs, err := json.Marshal(bashArgs{Command: "printf nope", Background: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.bash(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: string(rawArgs)}})
	if err == nil || !strings.Contains(err.Error(), "unavailable in one-shot mode") {
		t.Fatalf("background error = %v", err)
	}
}

func processManagerForTest(t *testing.T) *processManager {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	manager, err := NewProcessManager(t.TempDir(), t.TempDir(), sandboxOff, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func runProcessTool[T any](t *testing.T, run func(context.Context, llm.ToolCall) (golem.ToolResult, error), args T) string {
	t.Helper()
	return runProcessResult(t, run, args).Content
}

func runProcessResult[T any](t *testing.T, run func(context.Context, llm.ToolCall) (golem.ToolResult, error), args T) golem.ToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: string(raw)}})
	if err != nil {
		t.Fatalf("tool error = %v", err)
	}
	return result
}

func jobIDFromText(t *testing.T, text string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^job_id: (job-[0-9a-f]+)$`).FindStringSubmatch(text)
	if len(match) != 2 {
		t.Fatalf("job id missing: %q", text)
	}
	return match[1]
}
