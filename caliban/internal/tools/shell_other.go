//go:build !linux

package tools

import (
	"fmt"
	"os"
	"os/exec"
)

// shellCommand builds plain bash: Landlock is Linux-only, so other platforms
// have no per-command sandbox. SandboxRequire fails closed rather than running
// unsandboxed; SandboxAuto/Off run bash directly (trusted local mode). A macOS
// Seatbelt implementation could slot in here behind the same seam later.
func shellCommand(workdir, command, sandbox string) (*exec.Cmd, error) {
	if sandbox == SandboxRequire {
		return nil, fmt.Errorf("sandbox required but not supported on this platform")
	}
	cmd := exec.Command("bash", "-c", command)
	cmd.Env = scrubbedEnv(workdir)
	return cmd, nil
}

func trustedRunnerCommand(spec runnerCommandSpec) (*exec.Cmd, error) {
	if spec.Sandbox == SandboxRequire {
		return nil, fmt.Errorf("sandbox required but not supported on this platform")
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	return cmd, nil
}

// RunSandboxedShell is never reached on non-Linux (shellCommand never re-execs
// into it); it exists so main's dispatch and the package API stay platform-
// agnostic.
func RunSandboxedShell() {
	fmt.Fprintln(os.Stderr, "(sandbox: not supported on this platform)")
	os.Exit(126)
}

func RunSandboxedRunner() {
	fmt.Fprintln(os.Stderr, "(runner sandbox: not supported on this platform)")
	os.Exit(126)
}
