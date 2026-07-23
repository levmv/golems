//go:build linux

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

const sandboxChildArg = "__cy_sandbox_bash"

const (
	envSandboxCommand = "CY_INTERNAL_SANDBOX_COMMAND"
	envSandboxRoot    = "CY_INTERNAL_SANDBOX_ROOT"
	envSandboxHome    = "CY_INTERNAL_SANDBOX_HOME"
	envSandboxPolicy  = "CY_INTERNAL_SANDBOX_POLICY"
)

func runSandboxChildIfRequested() bool {
	if len(os.Args) < 2 || os.Args[1] != sandboxChildArg {
		return false
	}
	runSandboxedBash()
	return true
}

func sandboxedBashCommand(command, workspace, workdir, home, policy string) (*exec.Cmd, error) {
	if policy == sandboxOff {
		cmd := exec.Command("bash", "-lc", command)
		cmd.Dir = workdir
		cmd.Env = minimalToolEnv(home)
		return cmd, nil
	}
	cmd := exec.Command("/proc/self/exe", sandboxChildArg)
	cmd.Dir = workdir
	cmd.Env = sandboxControlEnv(command, workspace, home, policy)
	return cmd, nil
}

func sandboxControlEnv(command, workspace, home, policy string) []string {
	return append(minimalToolEnv(home),
		envSandboxCommand+"="+command,
		envSandboxRoot+"="+workspace,
		envSandboxHome+"="+home,
		envSandboxPolicy+"="+policy,
	)
}

func runSandboxedBash() {
	command := os.Getenv(envSandboxCommand)
	workspace := os.Getenv(envSandboxRoot)
	home := os.Getenv(envSandboxHome)
	policy := os.Getenv(envSandboxPolicy)
	bash, err := exec.LookPath("bash")
	if err != nil {
		bash = "/bin/bash"
	}
	runtime.LockOSThread()
	if err := applyToolLandlock(workspace, home, policy); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
		os.Exit(126)
	}
	if err := syscall.Exec(bash, []string{"bash", "-lc", command}, minimalToolEnv(home)); err != nil {
		fmt.Fprintf(os.Stderr, "sandbox exec bash: %v\n", err)
		os.Exit(126)
	}
}

func applyToolLandlock(workspace, home, policy string) error {
	rules := []landlock.Rule{
		landlock.RODirs("/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/opt", "/run/systemd/resolve").IgnoreIfMissing(),
		landlock.RWDirs("/tmp", "/var/tmp", workspace, home).IgnoreIfMissing(),
		landlock.RWFiles("/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom", "/dev/tty").IgnoreIfMissing(),
	}
	if policy == sandboxRequire {
		return landlock.V1.RestrictPaths(rules...)
	}
	return landlock.V9.BestEffort().RestrictPaths(rules...)
}

func hardenSupervisor() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) }
