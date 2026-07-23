//go:build !linux

package tools

import (
	"errors"
	"os/exec"
)

func runSandboxChildIfRequested() bool { return false }

func sandboxedBashCommand(command, workspace, workdir, home, policy string) (*exec.Cmd, error) {
	if policy == sandboxRequire {
		return nil, errors.New("Landlock is only available on Linux")
	}
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = workdir
	cmd.Env = minimalToolEnv(home)
	return cmd, nil
}

func sandboxControlEnv(command, workspace, home, policy string) []string {
	return minimalToolEnv(home)
}

func hardenSupervisor() {}
