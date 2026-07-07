package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

const (
	runnerWorkspaceReadOnly  = "read_only"
	runnerWorkspaceReadWrite = "read_write"
	defaultRunnerTimeout     = 10 * time.Minute
)

type runnerCommandSpec struct {
	Executable      string   `json:"executable"`
	Args            []string `json:"args"`
	Dir             string   `json:"dir"`
	Env             []string `json:"env"`
	Workdir         string   `json:"workdir"`
	WorkdirWritable bool     `json:"workdir_writable"`
	Sandbox         string   `json:"sandbox"`
	RODirs          []string `json:"ro_dirs,omitempty"`
	RWDirs          []string `json:"rw_dirs,omitempty"`
	ROFiles         []string `json:"ro_files,omitempty"`
	RWFiles         []string `json:"rw_files,omitempty"`
}

type RunnerManager struct {
	workdir    string
	home       string
	sandbox    string
	maxOutput  int
	background *BackgroundManager
	profiles   map[string]runnerProfile
}

type runnerProfile struct {
	Name        string
	Description string
	Executable  string
	Available   bool
	Missing     string
	StateDirs   []string
	StateFiles  []string
	ExecDirs    []string
	ExtraRODirs []string
	ModelsArgs  []string
}

type runnerRunArgs struct {
	Runner          string `json:"runner"`
	Prompt          string `json:"prompt"`
	Model           string `json:"model,omitempty"`
	Session         string `json:"session,omitempty"`
	WorkspaceAccess string `json:"workspace_access,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

type runnerModelsArgs struct {
	Runner         string `json:"runner"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func NewRunnerManager(workdir, sandbox string, maxOutput int, background *BackgroundManager) *RunnerManager {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if sandbox == "" {
		sandbox = SandboxAuto
	}
	if maxOutput <= 0 {
		maxOutput = 32768
	}
	m := &RunnerManager{
		workdir:    workdir,
		home:       home,
		sandbox:    sandbox,
		maxOutput:  maxOutput,
		background: background,
		profiles:   map[string]runnerProfile{},
	}
	for _, profile := range builtinRunnerProfiles(home) {
		m.profiles[profile.Name] = m.resolveProfile(profile)
	}
	return m
}

func RunnerTools(manager *RunnerManager) []golem.Tool {
	return []golem.Tool{
		runnerListTool(manager),
		runnerModelsTool(manager),
		runnerRunTool(manager),
	}
}

func builtinRunnerProfiles(home string) []runnerProfile {
	return []runnerProfile{
		{
			Name:        "agy",
			Description: "Google Antigravity CLI (`agy`) with its normal ~/.gemini/antigravity-cli state.",
			Executable:  "agy",
			StateDirs:   []string{filepath.Join(home, ".gemini", "antigravity-cli")},
			ExecDirs:    []string{filepath.Join(home, ".local", "bin")},
			ExtraRODirs: []string{filepath.Join(home, ".gemini")},
			ModelsArgs:  []string{"models"},
		},
		{
			Name:        "codex",
			Description: "OpenAI Codex CLI with its normal ~/.codex state.",
			Executable:  "codex",
			StateDirs:   []string{filepath.Join(home, ".codex")},
			ExecDirs:    []string{filepath.Join(home, ".local", "bin")},
		},
		{
			Name:        "claude",
			Description: "Claude Code CLI with its normal ~/.claude state.",
			Executable:  "claude",
			StateDirs: []string{
				filepath.Join(home, ".claude"),
				filepath.Join(home, ".local", "share", "claude"),
			},
			StateFiles: []string{filepath.Join(home, ".claude.json")},
			ExecDirs: []string{
				filepath.Join(home, ".local", "bin"),
				filepath.Join(home, ".local", "share", "claude"),
			},
			ExtraRODirs: []string{"/proc/self", "/proc/thread-self"},
		},
		{
			Name:        "pi",
			Description: "pi-agent CLI with its normal ~/.pi state.",
			Executable:  "pi",
			StateDirs:   []string{filepath.Join(home, ".pi")},
			ExecDirs: []string{
				filepath.Join(home, ".npm-global", "bin"),
				filepath.Join(home, ".npm-global", "lib", "node_modules"),
			},
			ModelsArgs: []string{"--list-models"},
		},
	}
}

func (m *RunnerManager) resolveProfile(profile runnerProfile) runnerProfile {
	path, err := findRunnerExecutable(m.home, profile.Executable)
	if err != nil {
		profile.Available = false
		profile.Missing = err.Error()
		return profile
	}
	profile.Executable = path
	profile.Available = true
	if real, err := filepath.EvalSymlinks(path); err == nil {
		profile.ExecDirs = append(profile.ExecDirs, filepath.Dir(real))
	}
	profile.ExecDirs = append(profile.ExecDirs, filepath.Dir(path))
	profile.ExecDirs = cleanExistingPaths(profile.ExecDirs)
	profile.StateDirs = cleanExistingPaths(profile.StateDirs)
	profile.StateFiles = cleanExistingPaths(profile.StateFiles)
	return profile
}

func findRunnerExecutable(home, name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	candidates := []string{
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".npm-global", "bin", name),
		filepath.Join("/usr", "local", "bin", name),
		filepath.Join("/usr", "bin", name),
		filepath.Join("/bin", name),
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH or standard user bin dirs", name)
}

func cleanExistingPaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

func runnerListTool(manager *RunnerManager) golem.Tool {
	schema := jsonschema.Obj()
	return golem.FunctionTool("runner_list",
		"List trusted external agent runners available to Caliban.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "runner tools are not enabled"}, nil
			}
			return golem.ToolResult{Content: manager.formatList()}, nil
		})
}

func runnerModelsTool(manager *RunnerManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("runner", jsonschema.Str{
			Description: "Runner name, for example agy or pi.",
		}),
		jsonschema.Optional("timeout_seconds", jsonschema.Int{
			Description: "Optional timeout in seconds. Defaults to 10 minutes.",
		}),
	)
	return golem.FunctionTool("runner_models",
		"List models for a trusted runner when the runner exposes a non-interactive model-list command.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "runner tools are not enabled"}, nil
			}
			var args runnerModelsArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid runner_models arguments: %w", err)
			}
			out, err := manager.RunModels(ctx, args.Runner, timeoutFromSeconds(args.TimeoutSeconds))
			if err != nil {
				return golem.ToolResult{Content: fmt.Sprintf("(runner_models failed: %v)", err)}, nil
			}
			return golem.ToolResult{Content: out}, nil
		})
}

func runnerRunTool(manager *RunnerManager) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("runner", jsonschema.Str{
			Description: "Runner name: agy, codex, claude, or pi.",
		}),
		jsonschema.Required("prompt", jsonschema.Str{
			Description: "Task prompt to send to the external runner.",
		}),
		jsonschema.Optional("model", jsonschema.Str{
			Description: "Optional model name/id passed through the runner's model flag.",
		}),
		jsonschema.Optional("session", jsonschema.Str{
			Description: `Session mode. Use "new" or omit for a new task, "continue" for the runner's latest session, or an exact session/conversation id when known.`,
		}),
		jsonschema.Optional("workspace_access", jsonschema.Str{
			Description: `Workspace filesystem access for the runner process: "read_only" (default) or "read_write".`,
		}),
		jsonschema.Optional("timeout_seconds", jsonschema.Int{
			Description: "Optional timeout in seconds. Defaults to 10 minutes.",
		}),
		jsonschema.Optional("run_in_background", jsonschema.Bool{
			Description: "Start the runner as a managed background task and return a task id immediately.",
		}),
	)
	return golem.FunctionTool("runner_run",
		"Run a task through a trusted external agent runner with semantic controls, not arbitrary argv.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if manager == nil {
				return golem.ToolResult{Content: "runner tools are not enabled"}, nil
			}
			var args runnerRunArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid runner_run arguments: %w", err)
			}
			out, err := manager.Run(ctx, args)
			if err != nil {
				return golem.ToolResult{Content: fmt.Sprintf("(runner_run failed: %v)", err)}, nil
			}
			return golem.ToolResult{Content: out}, nil
		})
}

func (m *RunnerManager) formatList() string {
	var b strings.Builder
	b.WriteString("trusted runners:\n")
	for _, spec := range builtinRunnerProfiles(m.home) {
		profile, ok := m.profiles[spec.Name]
		if !ok {
			continue
		}
		status := "available"
		if !profile.Available {
			status = "missing"
		}
		fmt.Fprintf(&b, "- %s status=%s", profile.Name, status)
		if profile.Available {
			fmt.Fprintf(&b, " executable=%s", profile.Executable)
			if len(profile.ModelsArgs) > 0 {
				b.WriteString(" models=true")
			}
		} else {
			fmt.Fprintf(&b, " reason=%q", profile.Missing)
		}
		fmt.Fprintf(&b, "\n  %s\n", profile.Description)
	}
	b.WriteString("session: omit/new, continue, or exact id. workspace_access: read_only or read_write.")
	return b.String()
}

func (m *RunnerManager) RunModels(ctx context.Context, name string, timeout time.Duration) (string, error) {
	profile, err := m.profile(name)
	if err != nil {
		return "", err
	}
	if len(profile.ModelsArgs) == 0 {
		return fmt.Sprintf("runner %q does not expose a non-interactive model listing command in Caliban yet", profile.Name), nil
	}
	spec := m.commandSpec(profile, profile.ModelsArgs, false)
	cmd, err := trustedRunnerCommand(spec)
	if err != nil {
		return "", err
	}
	return sanitizeRunnerOutput(profile.Name, runProcess(ctx, cmd, timeout, m.maxOutput)), nil
}

func (m *RunnerManager) Run(ctx context.Context, args runnerRunArgs) (string, error) {
	profile, err := m.profile(args.Runner)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(args.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is required")
	}
	workspaceWritable, err := parseWorkspaceAccess(args.WorkspaceAccess)
	if err != nil {
		return "", err
	}
	timeout := timeoutFromSeconds(args.TimeoutSeconds)

	var resultPath string
	if profile.Name == "codex" && !args.RunInBackground {
		f, err := os.CreateTemp("", "caliban-codex-result-*.txt")
		if err != nil {
			return "", fmt.Errorf("create codex result file: %w", err)
		}
		resultPath = f.Name()
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close codex result file: %w", err)
		}
		defer os.Remove(resultPath)
	}

	argv, err := m.buildRunArgs(profile, args, guardedRunnerPrompt(prompt), workspaceWritable, resultPath)
	if err != nil {
		return "", err
	}
	spec := m.commandSpec(profile, argv, workspaceWritable)
	display := displayCommand(profile.Executable, argv)
	cmd, err := trustedRunnerCommand(spec)
	if err != nil {
		return "", err
	}
	if args.RunInBackground {
		if m.background == nil {
			return "runner background tasks are not enabled", nil
		}
		task, err := m.background.StartProcess(ctx, "runner", display, profile.Name+" runner", cmd, timeout)
		if err != nil {
			return "", err
		}
		return formatRunnerBackgroundStart(profile.Name, task), nil
	}
	out := runProcess(ctx, cmd, timeout, m.maxOutput)
	if resultPath != "" {
		if b, err := os.ReadFile(resultPath); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b)), nil
		}
	}
	return sanitizeRunnerOutput(profile.Name, out), nil
}

func (m *RunnerManager) profile(name string) (runnerProfile, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return runnerProfile{}, fmt.Errorf("runner is required")
	}
	profile, ok := m.profiles[name]
	if !ok {
		return runnerProfile{}, fmt.Errorf("unknown runner %q", name)
	}
	if !profile.Available {
		return runnerProfile{}, fmt.Errorf("runner %q is not available: %s", name, profile.Missing)
	}
	return profile, nil
}

func (m *RunnerManager) buildRunArgs(profile runnerProfile, args runnerRunArgs, prompt string, workspaceWritable bool, resultPath string) ([]string, error) {
	session := strings.TrimSpace(args.Session)
	model := strings.TrimSpace(args.Model)
	codexOutputPath := strings.TrimSpace(resultPath)
	switch profile.Name {
	case "agy":
		argv := []string{"--dangerously-skip-permissions"}
		if session == "continue" {
			argv = append(argv, "--continue")
		} else if sessionID := explicitSessionID(session); sessionID != "" {
			argv = append(argv, "--conversation", sessionID)
		}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		return append(argv, "--print", prompt), nil
	case "codex":
		argv := []string{"-a", "never"}
		addCodexOptions := func(argv []string) []string {
			if model != "" {
				argv = append(argv, "-m", model)
			}
			if codexOutputPath != "" {
				argv = append(argv, "-o", codexOutputPath)
			}
			return argv
		}
		if sessionID := explicitSessionID(session); session == "continue" || sessionID != "" {
			argv = append(argv, "exec", "resume", "--skip-git-repo-check")
			argv = addCodexOptions(argv)
			if session == "continue" {
				argv = append(argv, "--last")
			} else {
				argv = append(argv, sessionID)
			}
			return append(argv, prompt), nil
		} else {
			sandboxMode := "read-only"
			if workspaceWritable {
				sandboxMode = "workspace-write"
			}
			argv = append(argv, "exec", "--skip-git-repo-check", "-C", m.workdir, "-s", sandboxMode)
		}
		argv = addCodexOptions(argv)
		return append(argv, prompt), nil
	case "claude":
		argv := []string{"--print", "--output-format", "text"}
		if session == "continue" {
			argv = append(argv, "--continue")
		} else if sessionID := explicitSessionID(session); sessionID != "" {
			argv = append(argv, "--resume", sessionID)
		}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		if !workspaceWritable {
			argv = append(argv,
				"--tools=Read,Grep,Glob,LS",
				"--allowedTools=Read,Grep,Glob,LS",
			)
		} else {
			argv = append(argv,
				"--allowedTools=Read,Grep,Glob,LS,Edit,Write,Bash",
				"--permission-mode", "acceptEdits",
			)
		}
		return append(argv, prompt), nil
	case "pi":
		argv := []string{"--print", "--mode", "text", "--approve"}
		if session == "continue" {
			argv = append(argv, "--continue")
		} else if sessionID := explicitSessionID(session); sessionID != "" {
			argv = append(argv, "--session", sessionID)
		}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		if !workspaceWritable {
			argv = append(argv, "--tools", "read,grep,find,ls")
		} else {
			argv = append(argv, "--tools", "read,grep,find,ls,bash,edit,write")
		}
		return append(argv, prompt), nil
	default:
		return nil, fmt.Errorf("runner %q has no run mapping", profile.Name)
	}
}

func (m *RunnerManager) commandSpec(profile runnerProfile, argv []string, workspaceWritable bool) runnerCommandSpec {
	env := runnerEnv(m.home, m.workdir)
	spec := runnerCommandSpec{
		Executable:      profile.Executable,
		Args:            argv,
		Dir:             m.workdir,
		Env:             env,
		Workdir:         m.workdir,
		WorkdirWritable: workspaceWritable,
		Sandbox:         m.sandbox,
		RODirs:          append([]string{}, profile.ExecDirs...),
		RWDirs:          append([]string{}, profile.StateDirs...),
		RWFiles:         append([]string{}, profile.StateFiles...),
	}
	spec.RODirs = append(spec.RODirs, profile.ExtraRODirs...)
	if profile.Name == "agy" {
		if pathExists(filepath.Join(m.workdir, ".gemini")) {
			spec.RWDirs = append(spec.RWDirs, filepath.Join(m.workdir, ".gemini"))
		}
	}
	return spec
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runnerEnv(home, workdir string) []string {
	env := []string{
		"HOME=" + home,
		"PWD=" + workdir,
	}
	allow := []string{"PATH", "LANG", "TZ", "TMPDIR", "USER", "LOGNAME", "SHELL", "TERM", "NO_COLOR"}
	for _, key := range allow {
		if v, ok := os.LookupEnv(key); ok {
			if key == "PATH" {
				v = augmentRunnerPath(home, v)
			}
			env = append(env, key+"="+v)
		}
	}
	if _, ok := os.LookupEnv("PATH"); !ok {
		env = append(env, "PATH="+augmentRunnerPath(home, "/usr/local/bin:/usr/bin:/bin"))
	}
	return env
}

func augmentRunnerPath(home, current string) string {
	additions := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
	parts := strings.Split(current, ":")
	seen := map[string]bool{}
	for _, part := range parts {
		seen[part] = true
	}
	for _, add := range additions {
		if add != "" && !seen[add] {
			parts = append([]string{add}, parts...)
			seen[add] = true
		}
	}
	return strings.Join(parts, ":")
}

func parseWorkspaceAccess(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", runnerWorkspaceReadOnly:
		return false, nil
	case runnerWorkspaceReadWrite:
		return true, nil
	default:
		return false, fmt.Errorf("invalid workspace_access %q (use read_only or read_write)", s)
	}
}

func timeoutFromSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultRunnerTimeout
	}
	return time.Duration(seconds) * time.Second
}

func explicitSessionID(session string) string {
	session = strings.TrimSpace(session)
	switch strings.ToLower(session) {
	case "", "new", "continue":
		return ""
	}
	return strings.TrimPrefix(session, "id:")
}

func guardedRunnerPrompt(prompt string) string {
	return "You are being invoked by Caliban as a trusted external runner. Work on the requested task in the workspace. Do not inspect, print, or modify your own runner credentials, auth tokens, config, cache, history, logs, or session storage unless the user explicitly asks for that.\n\nTask:\n" + prompt
}

func displayCommand(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, executable)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func formatRunnerBackgroundStart(runner string, task BackgroundTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "runner task started\n")
	fmt.Fprintf(&b, "runner: %s\n", runner)
	fmt.Fprintf(&b, "task_id: %s\n", task.ID)
	fmt.Fprintf(&b, "status: %s\n", task.Status)
	fmt.Fprintf(&b, "pid: %d\n", task.PID)
	fmt.Fprintf(&b, "output_path: %s\n", task.OutputPath)
	fmt.Fprintf(&b, "Use task_output with this task_id to read output, task_list to inspect running tasks, and task_stop to stop it.")
	return b.String()
}

func sanitizeRunnerOutput(runner, out string) string {
	if runner != "agy" {
		return out
	}
	lines := strings.Split(out, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, "third_party/tcmalloc/parameters.cc") &&
			strings.Contains(line, "Using per-thread caches requires linking against") {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}
