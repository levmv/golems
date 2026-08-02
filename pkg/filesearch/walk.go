package filesearch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type fileWalker struct {
	searcher *Searcher
	query    resolvedDirectory
	direct   *directPattern
	ignore   *ignoreMatcher
	stats    Stats
	visit    func(File) error
}

type inspectedEntry struct {
	entry os.DirEntry
	mode  os.FileMode
}

func (w *fileWalker) run(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return w.stats, err
	}
	if w.query.rel != "" && pathInsideGitMetadata(w.query.rel) {
		return w.stats, nil
	}
	if w.query.rel != "" && w.directExcludes(w.query.rel, true) {
		return w.stats, nil
	}
	if w.searcher.ignoreMode == IgnoreRepository {
		w.ignore = newIgnoreMatcher()
		if err := w.ignore.loadRoot(w.searcher.root); err != nil {
			return w.stats, err
		}
		if err := w.loadAncestorIgnores(); err != nil {
			return w.stats, err
		}
		if w.query.rel != "" && w.ignore.ignored(w.query.rel, true) && !w.directReopens(w.query.rel, true) {
			return w.stats, nil
		}
	}

	err := w.walkDirectory(ctx, w.query.abs, w.query.rel, w.query.rel == "")
	if errors.Is(err, ErrStop) {
		w.stats.Stopped = true
		return w.stats, nil
	}
	return w.stats, err
}

func (w *fileWalker) loadAncestorIgnores() error {
	if w.query.rel == "" {
		return nil
	}
	parts := strings.Split(w.query.rel, "/")
	currentAbs := w.searcher.root
	currentRel := ""
	// loadRoot already loaded the root. The starting directory loads its own
	// files in walkDirectory, so only intermediate ancestors belong here.
	for _, part := range parts[:len(parts)-1] {
		currentAbs = filepath.Join(currentAbs, filepath.FromSlash(part))
		currentRel = joinRelative(currentRel, part)
		if err := w.ignore.loadDirectory(currentAbs, currentRel); err != nil {
			return err
		}
	}
	return nil
}

func (w *fileWalker) walkDirectory(ctx context.Context, abs, rel string, rootAlreadyLoaded bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, readErr := os.ReadDir(abs)
	if readErr != nil {
		return fmt.Errorf("filesearch: read directory %s: %w", abs, readErr)
	}
	if w.ignore != nil && !rootAlreadyLoaded {
		if loadErr := w.ignore.loadDirectoryEntries(abs, rel, entries); loadErr != nil {
			return loadErr
		}
	}
	inspected := make([]inspectedEntry, 0, len(entries))
	for _, entry := range entries {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		name := entry.Name()
		if name == ".git" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Info both fills in filesystems whose directory entries have an unknown
		// type and rechecks the path after ReadDir. It reports a symlink itself,
		// not its target, so an unknown-type link remains excluded.
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("filesearch: inspect path %s: %w", filepath.Join(abs, name), infoErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		inspected = append(inspected, inspectedEntry{entry: entry, mode: info.Mode()})
	}
	// Compare directories as though their names ended in the slash that their
	// descendants have in emitted paths. This makes depth-first streaming
	// globally lexical (for example, a.go sorts before a/file.go).
	sort.Slice(inspected, func(i, j int) bool { return inspectedEntryLess(inspected[i], inspected[j]) })

	for _, candidate := range inspected {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		name := candidate.entry.Name()
		entryRel := joinRelative(rel, name)
		if candidate.mode.IsDir() {
			if w.directExcludes(entryRel, true) {
				continue
			}
			if w.ignore != nil && w.ignore.ignored(entryRel, true) && !w.directReopens(entryRel, true) {
				continue
			}
			childAbs := filepath.Join(abs, name)
			// Recheck immediately before recursion: the entry may have been
			// replaced after the snapshot used for sorting and filtering.
			currentInfo, statErr := os.Lstat(childAbs)
			if statErr != nil {
				return fmt.Errorf("filesearch: inspect path %s: %w", childAbs, statErr)
			}
			if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() {
				continue
			}
			if walkErr := w.walkDirectory(ctx, childAbs, entryRel, false); walkErr != nil {
				return walkErr
			}
			continue
		}
		if !candidate.mode.IsRegular() {
			continue
		}
		w.stats.FilesVisited++
		if !w.includeFile(entryRel) {
			w.stats.FilesSkipped++
			continue
		}
		if visitErr := w.visit(File{Path: entryRel}); visitErr != nil {
			return visitErr
		}
		w.stats.Results++
	}
	return nil
}

func inspectedEntryLess(left, right inspectedEntry) bool {
	leftName := left.entry.Name()
	rightName := right.entry.Name()
	commonLength := min(len(leftName), len(rightName))
	if comparison := strings.Compare(leftName[:commonLength], rightName[:commonLength]); comparison != 0 {
		return comparison < 0
	}
	if len(leftName) == len(rightName) {
		return false
	}
	if len(leftName) < len(rightName) {
		if !left.mode.IsDir() {
			return true
		}
		return '/' < rightName[len(leftName)]
	}
	if !right.mode.IsDir() {
		return false
	}
	return leftName[len(rightName)] < '/'
}

func (w *fileWalker) includeFile(path string) bool {
	if w.direct == nil {
		return w.ignore == nil || !w.ignore.ignored(path, false)
	}
	directMatch := w.direct.matches(path, false)
	if w.direct.include {
		return directMatch
	}
	if directMatch {
		return false
	}
	return w.ignore == nil || !w.ignore.ignored(path, false)
}

func (w *fileWalker) directReopens(path string, isDir bool) bool {
	if w.direct != nil && w.direct.include {
		return w.direct.matches(path, isDir)
	}
	return false
}

func (w *fileWalker) directExcludes(path string, isDir bool) bool {
	if w.direct != nil && !w.direct.include {
		return w.direct.matches(path, isDir)
	}
	return false
}
