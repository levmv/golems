package tools

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"path/filepath"
)

const (
	sandboxAuto = "auto"
	sandboxOn   = "on"
	sandboxOff  = "off"
)

func RunSandboxChildIfRequested() bool { return runSandboxChildIfRequested() }

func HardenSupervisor() { hardenSupervisor() }

func SandboxBackend() string { return sandboxBackend() }

func SandboxedBashCommand(command, workspace, workdir, home, policy string) (*exec.Cmd, error) {
	return sandboxedBashCommand(command, workspace, workdir, home, policy)
}

func ambientBashCommand(command, workdir string) *exec.Cmd {
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = workdir
	return cmd
}

func WorkspaceToolHome(home, root string) string {
	digest := sha256.Sum256([]byte(root))
	return filepath.Join(home, "tool-cache", fmt.Sprintf("%x", digest[:8]))
}
