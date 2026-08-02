package filesearch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type resolvedDirectory struct {
	abs string
	rel string
}

type resolvedPath struct {
	abs      string
	resolved string
	rel      string
	info     os.FileInfo
}

func (s *Searcher) resolveDirectory(path string) (resolvedDirectory, error) {
	resolved, err := s.resolveExistingPath(path)
	if err != nil {
		return resolvedDirectory{}, err
	}
	if !resolved.info.IsDir() {
		return resolvedDirectory{}, fmt.Errorf("filesearch: path is not a directory: %s", path)
	}
	return resolvedDirectory{abs: resolved.abs, rel: resolved.rel}, nil
}

func (s *Searcher) resolveExistingPath(path string) (resolvedPath, error) {
	if s == nil || s.root == "" {
		return resolvedPath{}, errors.New("filesearch: uninitialized Searcher")
	}
	if path == "" {
		path = "."
	}
	osPath := filepath.FromSlash(path)
	if filepath.IsAbs(osPath) || filepath.VolumeName(osPath) != "" {
		return resolvedPath{}, fmt.Errorf("filesearch: absolute paths are not allowed: %s", path)
	}
	clean := filepath.Clean(osPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return resolvedPath{}, fmt.Errorf("filesearch: path escapes search root: %s", path)
	}
	abs := filepath.Join(s.root, clean)
	if !withinRoot(s.root, abs) {
		return resolvedPath{}, fmt.Errorf("filesearch: path escapes search root: %s", path)
	}

	// Explicit query paths may contain symlinks, matching ordinary command-line
	// behavior. Resolve them once to enforce the root boundary; traversal below
	// the explicit path still does not follow discovered symlink entries.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("filesearch: resolve path %q: %w", path, err)
	}
	if !withinRoot(s.root, resolved) {
		return resolvedPath{}, fmt.Errorf("filesearch: symlink target escapes search root: %s", path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("filesearch: stat path %q: %w", path, err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return resolvedPath{}, fmt.Errorf("filesearch: path is not a regular file or directory: %s", path)
	}
	rel, err := filepath.Rel(s.root, abs)
	if err != nil {
		return resolvedPath{}, fmt.Errorf("filesearch: make path relative: %w", err)
	}
	return resolvedPath{
		abs:      abs,
		resolved: resolved,
		rel:      cleanRelativePath(rel),
		info:     info,
	}, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func cleanRelativePath(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." {
		return ""
	}
	return strings.TrimPrefix(path, "./")
}

func joinRelative(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func pathInsideGitMetadata(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}
