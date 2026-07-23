package tools

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
)

const (
	sandboxAuto    = "auto"
	sandboxRequire = "require"
	sandboxOff     = "off"
)

func RunSandboxChildIfRequested() bool { return runSandboxChildIfRequested() }

func HardenSupervisor() { hardenSupervisor() }

func SandboxedBashCommand(command, workspace, workdir, home, policy string) (*exec.Cmd, error) {
	return sandboxedBashCommand(command, workspace, workdir, home, policy)
}

func SandboxControlEnv(command, workspace, home, policy string) []string {
	return sandboxControlEnv(command, workspace, home, policy)
}

func WorkspaceToolHome(home, root string) string {
	digest := sha256.Sum256([]byte(root))
	return filepath.Join(home, "tool-cache", fmt.Sprintf("%x", digest[:8]))
}
