package filesearch

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ignoreMatcher struct {
	vcs *ruleMatcher
	dot *ruleMatcher
	rg  *ruleMatcher
}

func newIgnoreMatcher() *ignoreMatcher {
	return &ignoreMatcher{
		vcs: newRuleMatcher(),
		dot: newRuleMatcher(),
		rg:  newRuleMatcher(),
	}
}

func (m *ignoreMatcher) ignored(path string, isDir bool) bool {
	// Source precedence is independent of directory depth. Within one source,
	// the dependency's scoped last-match-wins behavior handles parent/child
	// precedence. A higher-priority negation must also stop the lookup.
	for _, matcher := range []*ruleMatcher{m.rg, m.dot, m.vcs} {
		matched, ignored := matcher.decision(path, isDir)
		if matched {
			return ignored
		}
	}
	return false
}

func (m *ignoreMatcher) addFile(matcher *ruleMatcher, abs, relDir string) error {
	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("filesearch: inspect ignore file %s: %w", abs, err)
	}
	// Ignore files encountered during traversal obey the same no-symlink
	// boundary as ordinary entries. Git also deliberately does not follow a
	// symlink in place of .gitignore.
	if !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("filesearch: read ignore file %s: %w", abs, err)
	}
	for lineNumber, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) >= bufio.MaxScanTokenSize {
			return fmt.Errorf("filesearch: ignore pattern in %s exceeds %d bytes", abs, bufio.MaxScanTokenSize-1)
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		expanded, expandErr := expandIgnoreLine(string(line))
		if expandErr != nil {
			return fmt.Errorf("filesearch: invalid ignore pattern in %s:%d: %w", abs, lineNumber+1, expandErr)
		}
		for _, pattern := range expanded {
			if patternErrs := matcher.addPatterns([]byte(pattern), relDir); len(patternErrs) > 0 {
				first := patternErrs[0]
				return fmt.Errorf("filesearch: invalid ignore pattern in %s:%d: %s: %s", abs, lineNumber+1, first.Pattern, first.Message)
			}
		}
	}
	return nil
}

func expandIgnoreLine(line string) ([]string, error) {
	if line == "" || strings.HasPrefix(line, "#") {
		return []string{line}, nil
	}
	return expandBraces(line)
}

func (m *ignoreMatcher) loadRoot(root string) error {
	// In a linked worktree .git is a regular file whose target may live outside
	// the search root. Only read an exclude file reached through real, local
	// metadata directories.
	gitDir := filepath.Join(root, ".git")
	gitInfoDir := filepath.Join(gitDir, "info")
	gitDirOK, err := localDirectory(gitDir)
	if err != nil {
		return err
	}
	gitInfoDirOK := false
	if gitDirOK {
		gitInfoDirOK, err = localDirectory(gitInfoDir)
		if err != nil {
			return err
		}
	}
	if gitInfoDirOK {
		if addErr := m.addFile(m.vcs, filepath.Join(gitInfoDir, "exclude"), ""); addErr != nil {
			return addErr
		}
	}
	return m.loadDirectory(root, "")
}

func localDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("filesearch: inspect metadata directory %s: %w", path, err)
	}
	return info.IsDir(), nil
}

func (m *ignoreMatcher) loadDirectory(abs, rel string) error {
	// Root and explicit-query ancestors are loaded before their directory
	// entries are available. Recursive traversal uses loadDirectoryEntries.
	for _, source := range []struct {
		name    string
		matcher *ruleMatcher
	}{
		{name: ".gitignore", matcher: m.vcs},
		{name: ".ignore", matcher: m.dot},
		{name: ".rgignore", matcher: m.rg},
	} {
		if err := m.addFile(source.matcher, filepath.Join(abs, source.name), rel); err != nil {
			return err
		}
	}
	return nil
}

func (m *ignoreMatcher) loadDirectoryEntries(abs, rel string, entries []os.DirEntry) error {
	// Reuse the listing that walkDirectory already needs. Trying all three
	// names with os.ReadFile in every directory made missing ignore files a
	// significant source of syscalls on deep trees.
	for _, entry := range entries {
		var matcher *ruleMatcher
		switch entry.Name() {
		case ".gitignore":
			matcher = m.vcs
		case ".ignore":
			matcher = m.dot
		case ".rgignore":
			matcher = m.rg
		default:
			continue
		}
		if err := m.addFile(matcher, filepath.Join(abs, entry.Name()), rel); err != nil {
			return err
		}
	}
	return nil
}

func pathWithDirectorySuffix(path string, isDir bool) string {
	if isDir {
		return path + "/"
	}
	return path
}
