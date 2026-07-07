package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

// shellEnvAllowlist names the only parent environment variables passed through
// to shell commands. Secrets (Telegram token, LLM keys) live in the supervisor
// process and the config file, never in the shell's view.
var shellEnvAllowlist = []string{"PATH", "LANG", "TZ", "TMPDIR"}

// Sandbox policies for the shell tool.
const (
	SandboxRequire = "require" // fail closed if the sandbox is unavailable
	SandboxAuto    = "auto"    // sandbox when available, else run unsandboxed
	SandboxOff     = "off"     // never sandbox (trusted local mode)

	// SandboxArgv is the argv[1] caliban re-execs itself with to become the
	// sandboxed shell child (the Landlock trampoline). main dispatches on it.
	SandboxArgv = "__sandbox-shell"
	RunnerArgv  = "__sandbox-runner"

	envSandboxCmd      = "CALIBAN_SANDBOX_CMD"
	envSandboxWorkdir  = "CALIBAN_SANDBOX_WORKDIR"
	envSandboxPolicy   = "CALIBAN_SANDBOX_POLICY"
	envSandboxLocalBin = "CALIBAN_SANDBOX_LOCAL_BIN"
	envSandboxRunner   = "CALIBAN_SANDBOX_RUNNER"
)

type shellArgs struct {
	Command         string `json:"command"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
	IsBackground    bool   `json:"is_background,omitempty"` // accepted alias; not advertised in the schema
}

// Shell returns the `shell` tool: bash -c in workdir with a scrubbed
// environment. defaultTimeout applies when the call omits timeout_seconds;
// combined stdout+stderr is capped at maxOutput bytes. sandbox selects the
// per-command isolation policy (SandboxRequire/Auto/Off).
func Shell(workdir string, defaultTimeout time.Duration, maxOutput int, sandbox string, background *BackgroundManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("command", jsonschema.Str{
			Description: "Bash command to run in the workspace. Combined stdout+stderr is returned.",
		}),
		jsonschema.Optional("timeout_seconds", jsonschema.Int{
			Description: "Optional timeout in seconds. Foreground commands default to the configured shell timeout; background commands have no timeout unless this is set.",
		}),
		jsonschema.Optional("run_in_background", jsonschema.Bool{
			Description: "Start the command as a managed background task and return a task id immediately. Do not add trailing &.",
		}),
	)
	return golem.FunctionTool("shell",
		"Run a bash command in the agent's workspace with a scrubbed environment. "+
			"Use run_in_background=true for long-running commands. Non-zero exit codes are reported in the output, not as errors.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			var args shellArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid shell arguments: %w", err)
			}
			args.Command = strings.TrimSpace(args.Command)
			if args.Command == "" {
				return golem.ToolResult{}, fmt.Errorf("shell: command is required")
			}
			if err := validateManagedShellCommand(args.Command); err != nil {
				return golem.ToolResult{Content: fmt.Sprintf("(shell refused: %v)", err)}, nil
			}
			if args.RunInBackground || args.IsBackground {
				if background == nil {
					return golem.ToolResult{Content: "background shell tasks are not enabled"}, nil
				}
				var timeout time.Duration
				if args.TimeoutSeconds > 0 {
					timeout = time.Duration(args.TimeoutSeconds) * time.Second
				}
				task, err := background.StartShell(ctx, args.Command, timeout)
				if err != nil {
					return golem.ToolResult{Content: fmt.Sprintf("(failed to start background task: %v)", err)}, nil
				}
				return golem.ToolResult{Content: formatShellBackgroundStart(task)}, nil
			}
			timeout := defaultTimeout
			if args.TimeoutSeconds > 0 {
				timeout = time.Duration(args.TimeoutSeconds) * time.Second
			}
			out := runShell(ctx, workdir, args.Command, timeout, maxOutput, sandbox)
			return golem.ToolResult{Content: out}, nil
		})
}

func runShell(ctx context.Context, workdir, command string, timeout time.Duration, maxOutput int, sandbox string) string {
	// shellCommand is platform-specific: on Linux it builds the Landlock
	// trampoline (a re-exec of caliban that sandboxes itself then execs bash);
	// elsewhere, or with sandbox off, it builds plain bash. It sets cmd.Env.
	cmd, err := shellCommand(workdir, command, sandbox)
	if err != nil {
		return fmt.Sprintf("(shell unavailable: %v)", err)
	}
	cmd.Dir = workdir
	return runProcess(ctx, cmd, timeout, maxOutput)
}

func runProcess(ctx context.Context, cmd *exec.Cmd, timeout time.Duration, maxOutput int) string {
	// Own process group so a timeout can kill the whole tree, not just bash.
	// execve in the trampoline preserves the pgid, so this still targets bash.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// os/exec serializes writes when Stdout and Stderr are the same writer.
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return fmt.Sprintf("(failed to start: %v)", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var suffix string
	select {
	case err := <-done:
		suffix = exitSuffix(err)
	case <-timer.C:
		killGroup(cmd)
		<-done
		suffix = fmt.Sprintf("(killed: timeout after %s)", timeout)
	case <-ctx.Done():
		killGroup(cmd)
		<-done
		suffix = fmt.Sprintf("(killed: %v)", ctx.Err())
	}

	output := truncateMiddle(buf.Bytes(), maxOutput)
	if suffix == "" {
		return output
	}
	if output == "" {
		return suffix
	}
	return output + "\n" + suffix
}

// scrubbedEnv builds the command environment: the allow-listed parent vars that
// are set, plus HOME pinned to the workspace.
func scrubbedEnv(workdir string) []string {
	env := make([]string, 0, len(shellEnvAllowlist)+1)
	env = append(env, "HOME="+workdir)
	for _, key := range shellEnvAllowlist {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func formatShellBackgroundStart(task BackgroundTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "background task started\n")
	fmt.Fprintf(&b, "task_id: %s\n", task.ID)
	fmt.Fprintf(&b, "status: %s\n", task.Status)
	fmt.Fprintf(&b, "pid: %d\n", task.PID)
	fmt.Fprintf(&b, "output_path: %s\n", task.OutputPath)
	if task.TimeoutSeconds > 0 {
		fmt.Fprintf(&b, "timeout_seconds: %d\n", task.TimeoutSeconds)
	}
	fmt.Fprintf(&b, "Use task_output with this task_id to read output, task_list to inspect running tasks, and task_stop to stop it.")
	return b.String()
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group (set via Setpgid).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// exitSuffix maps a cmd.Wait error to a trailing note. A clean exit returns "".
func exitSuffix(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("(exit status %d)", exitErr.ExitCode())
	}
	return fmt.Sprintf("(error: %v)", err)
}

// truncateMiddle caps b at max bytes, keeping the head (2/3) and tail (1/3)
// with a marker between, so both the start and the end of long output survive.
func truncateMiddle(b []byte, max int) string {
	if max <= 0 || len(b) <= max {
		return string(b)
	}
	head := max * 2 / 3
	tail := max - head
	dropped := len(b) - head - tail
	marker := fmt.Sprintf("\n... [%d bytes truncated] ...\n", dropped)
	return string(b[:head]) + marker + string(b[len(b)-tail:])
}
