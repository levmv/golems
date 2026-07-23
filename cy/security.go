package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/cy/internal/state"
	toolruntime "github.com/levmv/golems/cy/internal/tools"
)

const (
	sandboxAuto    = "auto"
	sandboxRequire = "require"
	sandboxOff     = "off"
)

type SecurityState struct {
	Sandbox string
	Probe   string
}

func normalizeSandboxPolicy(policy string) (string, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" {
		policy = defaultSandboxPolicy
	}
	switch policy {
	case sandboxAuto, sandboxRequire, sandboxOff:
		return policy, nil
	default:
		return "", fmt.Errorf("unknown sandbox policy %q (want auto, require, or off)", policy)
	}
}

func buildSecurityState(ctx context.Context, cfg Config, root string, store *state.Store) SecurityState {
	toolHome := toolruntime.WorkspaceToolHome(resolveStateHome(cfg.Home), root)
	state := SecurityState{Sandbox: "off"}
	if cfg.SandboxPolicy == sandboxOff {
		state.Probe = "disabled by configuration"
		return state
	}
	probeDir := store.Dir()
	probe, err := os.CreateTemp(probeDir, ".landlock-probe-")
	if err != nil {
		state.Probe = "probe setup failed: " + err.Error()
		return state
	}
	probePath := probe.Name()
	_, _ = probe.WriteString("supervisor-only")
	_ = probe.Close()
	defer os.Remove(probePath)
	command := "if cat " + bashQuote(probePath) + " >/dev/null 2>&1; then exit 42; else exit 0; fi"
	cmd, err := toolruntime.SandboxedBashCommand(command, root, root, toolHome, cfg.SandboxPolicy)
	if err != nil {
		state.Probe = "sandbox command failed: " + err.Error()
		return state
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd = exec.CommandContext(probeCtx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = root
	cmd.Env = toolruntime.SandboxControlEnv(command, root, toolHome, cfg.SandboxPolicy)
	err = cmd.Run()
	if err == nil {
		state.Sandbox = "landlock"
		state.Probe = "sandbox child could not read supervisor auth path"
		return state
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 42 {
		state.Probe = "sandbox child could read supervisor auth path"
	} else {
		state.Probe = "sandbox probe failed: " + err.Error()
	}
	if cfg.SandboxPolicy == sandboxRequire {
		state.Sandbox = "unavailable (required)"
	}
	return state
}

func resolveStateHome(home string) string {
	resolved, err := session.ResolveHome(home)
	if err != nil {
		return home
	}
	return resolved
}

func bashQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (s SecurityState) Compact() string {
	return fmt.Sprintf("sandbox: %s · network: open", s.Sandbox)
}
