package filesearch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// IgnoreMode controls whether repository-local ignore files affect searches.
type IgnoreMode uint8

const (
	// IgnoreRepository applies .git/info/exclude, .gitignore, .ignore, and
	// .rgignore files found at or below the search root.
	IgnoreRepository IgnoreMode = iota
	// IgnoreNone disables ignore-file processing. Git metadata directories are
	// still excluded.
	IgnoreNone
)

// Options configures a Searcher.
type Options struct {
	Ignore IgnoreMode
}

// Searcher performs rooted file operations. It is immutable and safe for
// concurrent use; each operation builds its own traversal state.
type Searcher struct {
	root       string
	ignoreMode IgnoreMode
}

// ErrStop asks an operation to stop successfully. The returned Stats reports
// Stopped=true. A callback may return ErrStop directly or wrap it.
var ErrStop = errors.New("stop file search")

// New creates a Searcher rooted at root. An empty root means the current
// directory. The stored root is absolute and symlink-free.
func New(root string, options Options) (*Searcher, error) {
	if options.Ignore != IgnoreRepository && options.Ignore != IgnoreNone {
		return nil, fmt.Errorf("filesearch: invalid ignore mode %d", options.Ignore)
	}
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filesearch: resolve root: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("filesearch: resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("filesearch: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("filesearch: root is not a directory: %s", abs)
	}
	return &Searcher{root: filepath.Clean(abs), ignoreMode: options.Ignore}, nil
}

// Root returns the canonical absolute search root.
func (s *Searcher) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// FilesQuery selects regular files below Path using Glob. Path defaults to the
// search root and must name a directory within it. Glob is required. A
// positive glob may select repository-ignored paths; a leading ! excludes its
// matches while repository ignores remain in force.
type FilesQuery struct {
	Path string
	Glob string
}

// SearchQuery selects UTF-8 text lines matching Pattern. Path may name a file
// or directory and defaults to the search root. Include is an optional direct
// glob and, when positive, may select repository-ignored paths. PreviewBytes
// truncates returned Text to a valid UTF-8 prefix; zero returns the complete
// line.
type SearchQuery struct {
	Path         string
	Pattern      string
	Include      string
	PreviewBytes int
}

// File is a file enumeration result. Path is slash-separated and relative to
// the Searcher root.
type File struct {
	Path string
}

// Match is one matching line. Line is one-based. At most one Match is emitted
// per line even when Pattern occurs multiple times.
type Match struct {
	Path          string
	Line          int
	Text          string
	LineTruncated bool
}

// Stats summarizes an operation. FilesVisited counts regular files considered
// by the operation; FilesSkipped counts visited files rejected by ignore,
// glob, binary, or invalid-UTF-8 policy. OversizedLinesSkipped counts text
// lines omitted from Search because they exceeded its safety bound. Results
// counts callbacks that completed successfully.
type Stats struct {
	FilesVisited          int
	FilesSkipped          int
	OversizedLinesSkipped int
	Results               int
	Stopped               bool
}
