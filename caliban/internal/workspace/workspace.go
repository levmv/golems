// Package workspace provides access to the agent-facing file store: a
// directory holding PERSONA.md, MEMORY.md + memory/, projects/, and
// playground/. The agent edits these through its shell like any other files;
// workspace loads persona and the memory index for context assembly.
//
// M1 surface is deliberately tiny: create the root and read the two files the
// engine folds into the system prompt. Git auto-commit lands in M3.
package workspace

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const (
	personaFile     = "PERSONA.md"
	memoryIndexFile = "MEMORY.md"
)

// Workspace is the agent-facing file store rooted at a directory.
type Workspace struct {
	root       string
	gitEnabled bool
	// commitMu serializes Commit. Runs commit on different goroutines — a worker
	// per active conversation, plus the tasks queue (reflection, and later
	// free-time) — and `git add -A` plus commit is not concurrency-safe (index
	// lock contention). The lock makes each snapshot atomic.
	commitMu sync.Mutex
}

// MemoryUpsertResult describes a durable memory write.
type MemoryUpsertResult struct {
	Path         string
	Created      bool
	IndexUpdated bool
}

// Open ensures root exists (mkdir -p), initializes it as a git repository
// (best-effort), and returns a Workspace over it.
func Open(root string) (*Workspace, error) {
	if root == "" {
		return nil, errors.New("workspace: root path is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: create root: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve root: %w", err)
	}
	w := &Workspace{root: abs}
	if err := w.initGit(); err != nil {
		return nil, fmt.Errorf("workspace: init git: %w", err)
	}
	return w, nil
}

// Root returns the absolute workspace root.
func (w *Workspace) Root() string { return w.root }

// Persona returns the contents of PERSONA.md, or "" if the file is absent.
func (w *Workspace) Persona() (string, error) { return w.readOptional(personaFile) }

// WritePersona replaces PERSONA.md with content. The engine's self-reflection
// uses it to evolve the persona; the change is versioned by the workspace's git
// history like any other file, so a bad revision is always recoverable.
func (w *Workspace) WritePersona(content string) error {
	content = strings.TrimSpace(content) + "\n"
	if err := os.WriteFile(filepath.Join(w.root, personaFile), []byte(content), 0o644); err != nil {
		return fmt.Errorf("workspace: write %s: %w", personaFile, err)
	}
	return nil
}

// MemoryIndex returns the contents of MEMORY.md, or "" if the file is absent.
func (w *Workspace) MemoryIndex() (string, error) { return w.readOptional(memoryIndexFile) }

// UpsertMemory creates or replaces one durable memory fact file and keeps the
// MEMORY.md index pointing at it. The title, not a user-supplied filename, is
// the stable semantic key.
func (w *Workspace) UpsertMemory(title, body, summary string) (MemoryUpsertResult, error) {
	title = cleanMemoryText(title)
	body = strings.TrimSpace(body)
	summary = cleanMemoryText(summary)
	if title == "" {
		return MemoryUpsertResult{}, fmt.Errorf("workspace: memory title is required")
	}
	if body == "" {
		return MemoryUpsertResult{}, fmt.Errorf("workspace: memory body is required")
	}
	if summary == "" {
		summary = firstSentence(body)
	}
	summary = cleanMemoryText(summary)

	slug := memorySlug(title)
	relPath := filepath.ToSlash(filepath.Join("memory", slug+".md"))
	absPath := filepath.Join(w.root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return MemoryUpsertResult{}, fmt.Errorf("workspace: create memory dir: %w", err)
	}
	_, statErr := os.Stat(absPath)
	created := errors.Is(statErr, fs.ErrNotExist)
	if statErr != nil && !created {
		return MemoryUpsertResult{}, fmt.Errorf("workspace: stat %s: %w", relPath, statErr)
	}

	content := "# " + title + "\n\n" + body + "\n"
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return MemoryUpsertResult{}, fmt.Errorf("workspace: write %s: %w", relPath, err)
	}
	updated, err := w.upsertMemoryIndexLine(title, relPath, summary)
	if err != nil {
		return MemoryUpsertResult{}, err
	}
	return MemoryUpsertResult{Path: relPath, Created: created, IndexUpdated: updated}, nil
}

func (w *Workspace) readOptional(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(w.root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("workspace: read %s: %w", name, err)
	}
	return string(b), nil
}

func (w *Workspace) upsertMemoryIndexLine(title, relPath, summary string) (bool, error) {
	indexPath := filepath.Join(w.root, memoryIndexFile)
	raw, err := os.ReadFile(indexPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
		raw = nil
	}
	if err != nil {
		return false, fmt.Errorf("workspace: read %s: %w", memoryIndexFile, err)
	}

	line := fmt.Sprintf("- [%s](%s) - %s", title, relPath, summary)
	lines := splitIndexLines(string(raw))
	found := false
	changed := false
	out := make([]string, 0, len(lines)+1)
	for _, existing := range lines {
		if strings.Contains(existing, "]("+relPath+")") {
			if !found {
				out = append(out, line)
				changed = changed || existing != line
				found = true
			} else {
				changed = true
			}
			continue
		}
		out = append(out, existing)
	}
	if !found {
		out = append(out, line)
		changed = true
	}
	if !changed {
		return false, nil
	}
	content := strings.Join(out, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		return false, fmt.Errorf("workspace: write %s: %w", memoryIndexFile, err)
	}
	return true, nil
}

func splitIndexLines(s string) []string {
	raw := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func memorySlug(title string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(title), " "))
	var b strings.Builder
	lastDash := false
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "memory"
	}
	if len(base) > 48 {
		base = strings.TrimRight(base[:48], "-")
	}
	hash := sha1.Sum([]byte(normalized))
	return base + "-" + hex.EncodeToString(hash[:])[:8]
}

func cleanMemoryText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func firstSentence(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if len(s) > 160 {
		s = strings.TrimSpace(s[:160])
	}
	return s
}
