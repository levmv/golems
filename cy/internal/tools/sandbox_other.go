//go:build !linux && !darwin

package tools

import (
	"errors"
	"os/exec"
	"runtime"
)

func runSandboxChildIfRequested() bool { return false }

func sandboxBackend() string { return "" }

func sandboxedBashCommand(command, workspace, workdir, home, policy string) (*exec.Cmd, error) {
	if policy == sandboxOn {
		return nil, errors.New("sandbox is unavailable on " + runtime.GOOS)
	}
	return ambientBashCommand(command, workdir), nil
}

func hardenSupervisor() {}
