package tools

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

const (
	backgroundKindShell = "shell"

	BackgroundStatusRunning   = "running"
	BackgroundStatusCompleted = "completed"
	BackgroundStatusFailed    = "failed"
	BackgroundStatusKilled    = "killed"
	BackgroundStatusTimedOut  = "timed_out"
	BackgroundStatusLost      = "lost"

	defaultTaskListLimit  = 20
	defaultTaskOutputWait = 30 * time.Second
	maxTaskOutputBytes    = 256 * 1024
	maxTaskOutputWait     = 5 * time.Minute
)

type runInfoKey struct{}

type runInfo struct {
	ConversationID int64
	RunID          int64
}

// WithRunInfo annotates tool execution contexts with the conversation/run owner.
func WithRunInfo(ctx context.Context, conversationID, runID int64) context.Context {
	return context.WithValue(ctx, runInfoKey{}, runInfo{ConversationID: conversationID, RunID: runID})
}

func runInfoFromContext(ctx context.Context) runInfo {
	if info, ok := ctx.Value(runInfoKey{}).(runInfo); ok {
		return info
	}
	return runInfo{}
}

// BackgroundTask is the durable metadata for one managed background process.
type BackgroundTask struct {
	ID             string
	Kind           string
	Command        string
	Description    string
	Status         string
	ConversationID int64
	StartedByRunID int64
	PID            int
	ExitCode       *int
	Error          string
	StopReason     string
	OutputPath     string
	OutputOffset   int64
	Notified       bool
	TimeoutSeconds int
	StartedAt      time.Time
	FinishedAt     *time.Time
	UpdatedAt      time.Time
	OutputSize     int64
	UnreadBytes    int64
}

type BackgroundTaskOutput struct {
	Task       BackgroundTask
	Offset     int64
	NextOffset int64
	OutputSize int64
	Content    string
	Truncated  bool
}

type backgroundProcess struct {
	id   string
	cmd  *exec.Cmd
	done chan struct{}

	mu         sync.Mutex
	stopReason string
}

func (p *backgroundProcess) setStopReason(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopReason == "" {
		p.stopReason = reason
	}
}

func (p *backgroundProcess) getStopReason() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopReason
}

// BackgroundManager owns managed long-running shell processes and their logs.
type BackgroundManager struct {
	db        *sql.DB
	workdir   string
	outputDir string
	maxOutput int
	sandbox   string

	mu   sync.Mutex
	live map[string]*backgroundProcess
}

func NewBackgroundManager(db *sql.DB, workdir, outputDir string, maxOutput int, sandbox string) (*BackgroundManager, error) {
	if db == nil {
		return nil, fmt.Errorf("background manager: db is required")
	}
	if workdir == "" {
		return nil, fmt.Errorf("background manager: workdir is required")
	}
	if outputDir == "" {
		return nil, fmt.Errorf("background manager: output dir is required")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, fmt.Errorf("create background output dir: %w", err)
	}
	if maxOutput <= 0 {
		maxOutput = 32768
	}
	return &BackgroundManager{
		db:        db,
		workdir:   workdir,
		outputDir: outputDir,
		maxOutput: maxOutput,
		sandbox:   sandbox,
		live:      map[string]*backgroundProcess{},
	}, nil
}

// ReconcileStartup marks tasks left running by a previous process as lost. They
// may still exist at the OS level, but Caliban no longer owns their process ids
// or output offset reliably.
func (m *BackgroundManager) ReconcileStartup(ctx context.Context) error {
	now := unixMillisNow()
	_, err := m.db.ExecContext(ctx, `
UPDATE background_tasks
SET status = ?, error = ?, finished_at = ?, updated_at = ?
WHERE status = ?`,
		BackgroundStatusLost,
		"task was still running when caliban started; process ownership was lost",
		now,
		now,
		BackgroundStatusRunning)
	if err != nil {
		return fmt.Errorf("reconcile background tasks: %w", err)
	}
	return nil
}

func (m *BackgroundManager) StartShell(ctx context.Context, command string, timeout time.Duration) (BackgroundTask, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return BackgroundTask{}, fmt.Errorf("shell command is required")
	}
	if err := validateManagedShellCommand(command); err != nil {
		return BackgroundTask{}, err
	}

	cmd, err := shellCommand(m.workdir, command, m.sandbox)
	if err != nil {
		return BackgroundTask{}, fmt.Errorf("shell unavailable: %w", err)
	}
	return m.StartProcess(ctx, backgroundKindShell, command, shortCommand(command, 160), cmd, timeout)
}

func (m *BackgroundManager) StartProcess(ctx context.Context, kind, command, description string, cmd *exec.Cmd, timeout time.Duration) (BackgroundTask, error) {
	if strings.TrimSpace(kind) == "" {
		kind = "process"
	}
	if strings.TrimSpace(description) == "" {
		description = shortCommand(command, 160)
	}
	id, err := newTaskID(kind)
	if err != nil {
		return BackgroundTask{}, err
	}
	outputPath := filepath.Join(m.outputDir, id+".log")
	outputFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return BackgroundTask{}, fmt.Errorf("create task output file: %w", err)
	}
	return m.startProcess(ctx, id, kind, command, description, cmd, outputFile, outputPath, timeout)
}

func (m *BackgroundManager) startProcess(ctx context.Context, id, kind, command, description string, cmd *exec.Cmd, outputFile *os.File, outputPath string, timeout time.Duration) (BackgroundTask, error) {
	if cmd.Dir == "" {
		cmd.Dir = m.workdir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile

	if err := cmd.Start(); err != nil {
		outputFile.Close()
		return BackgroundTask{}, fmt.Errorf("failed to start: %w", err)
	}

	info := runInfoFromContext(ctx)
	now := unixMillisNow()
	timeoutSeconds := 0
	if timeout > 0 {
		timeoutSeconds = int(timeout.Round(time.Second) / time.Second)
	}
	_, err := m.db.ExecContext(ctx, `
INSERT INTO background_tasks (
    id, kind, command, description, status, conversation_id, started_by_run_id,
    pid, output_path, timeout_seconds, started_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		kind,
		command,
		description,
		BackgroundStatusRunning,
		info.ConversationID,
		info.RunID,
		cmd.Process.Pid,
		outputPath,
		timeoutSeconds,
		now,
		now)
	if err != nil {
		killGroup(cmd)
		_ = cmd.Wait()
		outputFile.Close()
		return BackgroundTask{}, fmt.Errorf("record background task: %w", err)
	}

	proc := &backgroundProcess{id: id, cmd: cmd, done: make(chan struct{})}
	m.putLive(proc)
	go m.waitShell(proc, timeout, outputFile)

	loadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, err := m.loadTask(loadCtx, id)
	if err != nil {
		return BackgroundTask{}, err
	}
	return task, nil
}

func (m *BackgroundManager) Stop(ctx context.Context, id, reason string) (BackgroundTask, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return BackgroundTask{}, false, fmt.Errorf("task_id is required")
	}
	if reason == "" {
		reason = "stopped by task_stop"
	}

	if proc := m.getLive(id); proc != nil {
		proc.setStopReason(reason)
		killGroup(proc.cmd)
		select {
		case <-proc.done:
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return BackgroundTask{}, true, ctx.Err()
		}
		task, err := m.loadTask(ctx, id)
		return task, true, err
	}

	task, err := m.loadTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return BackgroundTask{}, false, nil
	}
	if err != nil {
		return BackgroundTask{}, false, err
	}
	return task, true, nil
}

func (m *BackgroundManager) StopAll(ctx context.Context, reason string) error {
	if reason == "" {
		reason = "caliban shutdown"
	}
	procs := m.liveProcesses()
	for _, proc := range procs {
		proc.setStopReason(reason)
		killGroup(proc.cmd)
	}
	for _, proc := range procs {
		select {
		case <-proc.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *BackgroundManager) List(ctx context.Context, activeOnly bool, limit int) ([]BackgroundTask, error) {
	if limit <= 0 {
		limit = defaultTaskListLimit
	}
	if limit > 100 {
		limit = 100
	}

	query := `
SELECT id, kind, command, description, status, conversation_id, started_by_run_id,
       pid, exit_code, error, stop_reason, output_path, output_offset, notified,
       timeout_seconds, started_at, finished_at, updated_at
FROM background_tasks`
	args := []any{}
	if activeOnly {
		query += ` WHERE status = ?`
		args = append(args, BackgroundStatusRunning)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list background tasks: %w", err)
	}
	defer rows.Close()

	var tasks []BackgroundTask
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		m.attachOutputInfo(&task)
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (m *BackgroundManager) ReadOutput(ctx context.Context, id string, offset *int64, maxBytes int, block bool, wait time.Duration) (BackgroundTaskOutput, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return BackgroundTaskOutput{}, false, fmt.Errorf("task_id is required")
	}
	if maxBytes <= 0 {
		maxBytes = m.maxOutput
	}
	if maxBytes > maxTaskOutputBytes {
		maxBytes = maxTaskOutputBytes
	}

	if block {
		if wait <= 0 {
			wait = defaultTaskOutputWait
		}
		if wait > maxTaskOutputWait {
			wait = maxTaskOutputWait
		}
		if proc := m.getLive(id); proc != nil {
			timer := time.NewTimer(wait)
			select {
			case <-proc.done:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return BackgroundTaskOutput{}, true, ctx.Err()
			}
			timer.Stop()
		}
	}

	task, err := m.loadTask(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return BackgroundTaskOutput{}, false, nil
	}
	if err != nil {
		return BackgroundTaskOutput{}, false, err
	}

	readOffset := task.OutputOffset
	explicitOffset := offset != nil
	if explicitOffset {
		readOffset = *offset
	}
	if readOffset < 0 {
		readOffset = 0
	}

	content, nextOffset, outputSize, truncated, err := readTaskFile(task.OutputPath, readOffset, maxBytes)
	if err != nil {
		return BackgroundTaskOutput{}, true, err
	}
	task.OutputSize = outputSize
	if nextOffset > task.OutputSize {
		nextOffset = task.OutputSize
	}
	if !explicitOffset && nextOffset != task.OutputOffset {
		if _, err := m.db.ExecContext(ctx,
			`UPDATE background_tasks SET output_offset = ?, updated_at = ? WHERE id = ?`,
			nextOffset, unixMillisNow(), id); err != nil {
			return BackgroundTaskOutput{}, true, fmt.Errorf("update task output offset: %w", err)
		}
		task.OutputOffset = nextOffset
	}
	task.UnreadBytes = 0
	if task.OutputSize > task.OutputOffset {
		task.UnreadBytes = task.OutputSize - task.OutputOffset
	}
	return BackgroundTaskOutput{
		Task:       task,
		Offset:     readOffset,
		NextOffset: nextOffset,
		OutputSize: outputSize,
		Content:    content,
		Truncated:  truncated,
	}, true, nil
}

func (m *BackgroundManager) waitShell(proc *backgroundProcess, timeout time.Duration, outputFile *os.File) {
	waitDone := make(chan error, 1)
	go func() { waitDone <- proc.cmd.Wait() }()

	var err error
	status := BackgroundStatusCompleted
	stopReason := ""
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		select {
		case err = <-waitDone:
		case <-timer.C:
			stopReason = fmt.Sprintf("timeout after %s", timeout)
			proc.setStopReason(stopReason)
			killGroup(proc.cmd)
			err = <-waitDone
			status = BackgroundStatusTimedOut
		}
		timer.Stop()
	} else {
		err = <-waitDone
	}

	if closeErr := outputFile.Close(); closeErr != nil && err == nil {
		err = closeErr
	}

	if status != BackgroundStatusTimedOut {
		stopReason = proc.getStopReason()
		switch {
		case stopReason != "":
			status = BackgroundStatusKilled
		case err == nil:
			status = BackgroundStatusCompleted
		default:
			status = BackgroundStatusFailed
		}
	}

	exitCode := exitCodeFromError(err)
	errText := ""
	if err != nil && status != BackgroundStatusKilled && status != BackgroundStatusTimedOut {
		errText = err.Error()
	}

	now := unixMillisNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = m.db.ExecContext(ctx, `
UPDATE background_tasks
SET status = ?, exit_code = ?, error = ?, stop_reason = ?, finished_at = ?, updated_at = ?
WHERE id = ?`,
		status,
		nullableInt(exitCode),
		nullableString(errText),
		nullableString(stopReason),
		now,
		now,
		proc.id)

	m.removeLive(proc.id)
	close(proc.done)
}

func (m *BackgroundManager) putLive(proc *backgroundProcess) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.live[proc.id] = proc
}

func (m *BackgroundManager) getLive(id string) *backgroundProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live[id]
}

func (m *BackgroundManager) removeLive(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, id)
}

func (m *BackgroundManager) liveProcesses() []*backgroundProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*backgroundProcess, 0, len(m.live))
	for _, proc := range m.live {
		out = append(out, proc)
	}
	return out
}

func (m *BackgroundManager) loadTask(ctx context.Context, id string) (BackgroundTask, error) {
	row := m.db.QueryRowContext(ctx, `
SELECT id, kind, command, description, status, conversation_id, started_by_run_id,
       pid, exit_code, error, stop_reason, output_path, output_offset, notified,
       timeout_seconds, started_at, finished_at, updated_at
FROM background_tasks
WHERE id = ?`, id)
	task, err := scanTask(row)
	if err != nil {
		return BackgroundTask{}, err
	}
	m.attachOutputInfo(&task)
	return task, nil
}

func (m *BackgroundManager) attachOutputInfo(task *BackgroundTask) {
	info, err := os.Stat(task.OutputPath)
	if err != nil {
		return
	}
	task.OutputSize = info.Size()
	if task.OutputSize > task.OutputOffset {
		task.UnreadBytes = task.OutputSize - task.OutputOffset
	}
}

type taskScanner interface {
	Scan(dest ...any) error
}

func scanTask(scanner taskScanner) (BackgroundTask, error) {
	var task BackgroundTask
	var pid sql.NullInt64
	var exitCode sql.NullInt64
	var errText sql.NullString
	var stopReason sql.NullString
	var notified int
	var startedAt int64
	var finishedAt sql.NullInt64
	var updatedAt int64
	if err := scanner.Scan(
		&task.ID,
		&task.Kind,
		&task.Command,
		&task.Description,
		&task.Status,
		&task.ConversationID,
		&task.StartedByRunID,
		&pid,
		&exitCode,
		&errText,
		&stopReason,
		&task.OutputPath,
		&task.OutputOffset,
		&notified,
		&task.TimeoutSeconds,
		&startedAt,
		&finishedAt,
		&updatedAt,
	); err != nil {
		return BackgroundTask{}, err
	}
	if pid.Valid {
		task.PID = int(pid.Int64)
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		task.ExitCode = &code
	}
	if errText.Valid {
		task.Error = errText.String
	}
	if stopReason.Valid {
		task.StopReason = stopReason.String
	}
	task.Notified = notified != 0
	task.StartedAt = time.UnixMilli(startedAt).UTC()
	if finishedAt.Valid {
		t := time.UnixMilli(finishedAt.Int64).UTC()
		task.FinishedAt = &t
	}
	task.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return task, nil
}

func readTaskFile(path string, offset int64, maxBytes int) (string, int64, int64, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, 0, false, fmt.Errorf("open task output: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", offset, 0, false, fmt.Errorf("stat task output: %w", err)
	}
	size := info.Size()
	if offset > size {
		offset = size
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", offset, size, false, fmt.Errorf("seek task output: %w", err)
	}
	buf, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", offset, size, false, fmt.Errorf("read task output: %w", err)
	}
	truncated := len(buf) > maxBytes
	if truncated {
		buf = buf[:maxBytes]
	}
	nextOffset := offset + int64(len(buf))
	return string(buf), nextOffset, size, truncated, nil
}

func newTaskID(prefix string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(b[:]), nil
}

func validateManagedShellCommand(command string) error {
	if hasBareTrailingAmp(command) {
		return fmt.Errorf("do not end shell commands with bare &: use run_in_background=true instead")
	}
	return nil
}

func hasBareTrailingAmp(command string) bool {
	s := strings.TrimSpace(command)
	if !strings.HasSuffix(s, "&") || strings.HasSuffix(s, "&&") {
		return false
	}
	backslashes := 0
	for i := len(s) - 2; i >= 0 && s[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func exitCodeFromError(err error) *int {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func unixMillisNow() int64 {
	return time.Now().UTC().UnixMilli()
}

func shortCommand(command string, max int) string {
	command = strings.ReplaceAll(command, "\n", `\n`)
	if len(command) <= max {
		return command
	}
	if max <= 3 {
		return command[:max]
	}
	return command[:max-3] + "..."
}

type taskListArgs struct {
	ActiveOnly *bool `json:"active_only,omitempty"`
	Limit      int   `json:"limit,omitempty"`
}

type taskOutputArgs struct {
	TaskID         string `json:"task_id"`
	Offset         *int64 `json:"offset,omitempty"`
	MaxBytes       int    `json:"max_bytes,omitempty"`
	Block          bool   `json:"block,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type taskStopArgs struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason,omitempty"`
}

func BackgroundTaskTools(manager *BackgroundManager) []golem.Tool {
	return []golem.Tool{
		taskListTool(manager),
		taskOutputTool(manager),
		taskStopTool(manager),
	}
}

func taskListTool(manager *BackgroundManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Optional("active_only", jsonschema.Bool{
			Description: "When true, list only currently running tasks. Defaults to true.",
			Default:     true,
			HasDefault:  true,
		}),
		jsonschema.Optional("limit", jsonschema.Int{
			Description: "Maximum number of tasks to show. Defaults to 20; capped at 100.",
		}),
	)
	return golem.FunctionToolWithEffect(golem.ToolEffectRead, "task_list",
		"List managed background tasks started by shell(run_in_background=true).",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "background tasks are not enabled"}, nil
			}
			var args taskListArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid task_list arguments: %w", err)
			}
			activeOnly := true
			if args.ActiveOnly != nil {
				activeOnly = *args.ActiveOnly
			}
			tasks, err := manager.List(ctx, activeOnly, args.Limit)
			if err != nil {
				return golem.ToolResult{}, err
			}
			return golem.ToolResult{Content: formatTaskList(tasks, activeOnly)}, nil
		})
}

func taskOutputTool(manager *BackgroundManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("task_id", jsonschema.Str{
			Description: "Background task id returned by shell.",
		}),
		jsonschema.Optional("offset", jsonschema.Int{
			Description: "Byte offset to read from. Omit to continue from the remembered offset.",
		}),
		jsonschema.Optional("max_bytes", jsonschema.Int{
			Description: "Maximum output bytes to return. Defaults to the shell output cap; capped at 262144.",
		}),
		jsonschema.Optional("block", jsonschema.Bool{
			Description: "If true, wait for the task to finish or until timeout_seconds.",
		}),
		jsonschema.Optional("timeout_seconds", jsonschema.Int{
			Description: "When block is true, maximum time to wait. Defaults to 30 seconds; capped at 300.",
		}),
	)
	return golem.FunctionTool("task_output",
		"Read output from a managed background task.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "background tasks are not enabled"}, nil
			}
			var args taskOutputArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid task_output arguments: %w", err)
			}
			wait := time.Duration(args.TimeoutSeconds) * time.Second
			out, ok, err := manager.ReadOutput(ctx, args.TaskID, args.Offset, args.MaxBytes, args.Block, wait)
			if err != nil {
				return golem.ToolResult{}, err
			}
			if !ok {
				return golem.ToolResult{Content: fmt.Sprintf("background task %q not found", args.TaskID)}, nil
			}
			return golem.ToolResult{Content: formatTaskOutput(out)}, nil
		})
}

func taskStopTool(manager *BackgroundManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("task_id", jsonschema.Str{
			Description: "Background task id returned by shell.",
		}),
		jsonschema.Optional("reason", jsonschema.Str{
			Description: "Optional human-readable reason recorded on the task.",
		}),
	)
	return golem.FunctionTool("task_stop",
		"Stop a managed background task and its process group.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "background tasks are not enabled"}, nil
			}
			var args taskStopArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid task_stop arguments: %w", err)
			}
			task, ok, err := manager.Stop(ctx, args.TaskID, args.Reason)
			if err != nil {
				return golem.ToolResult{}, err
			}
			if !ok {
				return golem.ToolResult{Content: fmt.Sprintf("background task %q not found", args.TaskID)}, nil
			}
			return golem.ToolResult{Content: formatTaskStop(task)}, nil
		})
}

func formatTaskList(tasks []BackgroundTask, activeOnly bool) string {
	if len(tasks) == 0 {
		if activeOnly {
			return "no running background tasks"
		}
		return "no background tasks"
	}
	var b strings.Builder
	b.WriteString("background tasks:\n")
	for _, task := range tasks {
		fmt.Fprintf(&b, "- %s status=%s pid=%d started=%s output_bytes=%d unread_bytes=%d command=%q",
			task.ID,
			task.Status,
			task.PID,
			task.StartedAt.Format(time.RFC3339),
			task.OutputSize,
			task.UnreadBytes,
			shortCommand(task.Command, 120))
		if task.ExitCode != nil {
			fmt.Fprintf(&b, " exit_code=%d", *task.ExitCode)
		}
		if task.StopReason != "" {
			fmt.Fprintf(&b, " stop_reason=%q", task.StopReason)
		}
		if task.Error != "" {
			fmt.Fprintf(&b, " error=%q", task.Error)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatTaskOutput(out BackgroundTaskOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "task_id: %s\n", out.Task.ID)
	fmt.Fprintf(&b, "status: %s\n", out.Task.Status)
	if out.Task.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *out.Task.ExitCode)
	}
	if out.Task.StopReason != "" {
		fmt.Fprintf(&b, "stop_reason: %s\n", out.Task.StopReason)
	}
	if out.Task.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", out.Task.Error)
	}
	fmt.Fprintf(&b, "offset: %d\n", out.Offset)
	fmt.Fprintf(&b, "next_offset: %d\n", out.NextOffset)
	fmt.Fprintf(&b, "output_bytes: %d\n", out.OutputSize)
	fmt.Fprintf(&b, "output_path: %s\n", out.Task.OutputPath)
	if out.Truncated {
		b.WriteString("truncated: true\n")
	}
	b.WriteString("content:\n")
	if out.Content == "" {
		b.WriteString("(no new output)")
	} else {
		b.WriteString(out.Content)
	}
	return b.String()
}

func formatTaskStop(task BackgroundTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "task_id: %s\n", task.ID)
	fmt.Fprintf(&b, "status: %s\n", task.Status)
	if task.StopReason != "" {
		fmt.Fprintf(&b, "stop_reason: %s\n", task.StopReason)
	}
	if task.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *task.ExitCode)
	}
	if task.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", task.Error)
	}
	return strings.TrimRight(b.String(), "\n")
}
