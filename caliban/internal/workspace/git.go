package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// scaffoldDirs are created on a fresh workspace so the agent has a home for its
// files. PERSONA.md and MEMORY.md are left for the agent to write.
var scaffoldDirs = []string{"memory", "projects", "playground"}

// initGit makes the workspace a git repository (history, audit, backup of
// persona/memory drift). It is best-effort: if git is unavailable, file
// operations still work and commits become no-ops. Idempotent on an existing
// repo.
func (w *Workspace) initGit() error {
	if _, err := exec.LookPath("git"); err != nil {
		return nil // git absent: degrade to no-op commits
	}
	w.gitEnabled = true

	if _, err := os.Stat(filepath.Join(w.root, ".git")); err == nil {
		return nil // already a repo
	}
	if _, err := w.git("init", "-q"); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"user.name", "caliban"}, {"user.email", "caliban@localhost"}} {
		if _, err := w.git("config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	for _, d := range scaffoldDirs {
		if err := os.MkdirAll(filepath.Join(w.root, d), 0o755); err != nil {
			return fmt.Errorf("workspace: scaffold %s: %w", d, err)
		}
		keep := filepath.Join(w.root, d, ".gitkeep")
		if _, err := os.Stat(keep); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(keep, nil, 0o644); err != nil {
				return fmt.Errorf("workspace: scaffold %s: %w", d, err)
			}
		}
	}
	if _, err := w.git("add", "-A"); err != nil {
		return err
	}
	if _, err := w.git("commit", "-q", "-m", "init workspace"); err != nil {
		return err
	}
	return nil
}

// Commit stages and commits any pending workspace changes. It is a no-op when
// the tree is clean or git is unavailable, so it can be called after every run.
func (w *Workspace) Commit(message string) error {
	if !w.gitEnabled {
		return nil
	}
	w.commitMu.Lock()
	defer w.commitMu.Unlock()
	status, err := w.git("status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		return nil // nothing changed
	}
	if _, err := w.git("add", "-A"); err != nil {
		return err
	}
	if _, err := w.git("commit", "-q", "-m", message); err != nil {
		return err
	}
	return nil
}

// git runs a git command in the workspace root, returning combined output.
func (w *Workspace) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = w.root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}
