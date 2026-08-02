package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const (
	defaultListEntries    = 200
	maxListEntries        = 1000
	maxListDirOutputBytes = 256 * 1024
)

func (w *workspaceTools) ls(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return golem.ToolResult{}, err
	}
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	abs, display, info, err := w.resolveExistingPath(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if !info.IsDir() {
		return golem.ToolResult{}, fmt.Errorf("%s is not a directory", display)
	}
	// os.ReadDir returns entries in filename order, making offsets stable while
	// the directory itself remains unchanged.
	entries, err := os.ReadDir(abs)
	if err != nil {
		return golem.ToolResult{}, fmt.Errorf("list directory %s: %w", display, err)
	}
	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := clampLimit(args.Limit, defaultListEntries, maxListEntries)
	start := min(offset-1, len(entries))
	if start >= len(entries) {
		return golem.ToolResult{Content: "no entries\n"}, nil
	}
	lines := make([]string, 0, min(limit, len(entries)-start))
	usedBytes := 0
	nextIndex := start
	for nextIndex < len(entries) && len(lines) < limit {
		if err := ctx.Err(); err != nil {
			return golem.ToolResult{}, err
		}
		line, err := formatDirectoryEntry(abs, entries[nextIndex])
		if err != nil {
			return golem.ToolResult{}, fmt.Errorf("list directory %s: %w", display, err)
		}
		// Keep the byte cap while guaranteeing pagination makes progress even on
		// a filesystem with an unusually large single directory entry.
		if len(lines) > 0 && usedBytes+len(line)+1 > maxListDirOutputBytes {
			break
		}
		lines = append(lines, line)
		usedBytes += len(line) + 1
		nextIndex++
	}
	var output strings.Builder
	fmt.Fprintf(&output, "entries: %d\n", len(lines))
	if nextIndex < len(entries) {
		output.WriteString("truncated: true\n")
		fmt.Fprintf(&output, "continue: ls(path=%q, offset=%d, limit=%d)\n", display, nextIndex+1, limit)
	}
	output.WriteByte('\n')
	output.WriteString(strings.Join(lines, "\n"))
	output.WriteByte('\n')
	return golem.ToolResult{Content: output.String()}, nil
}

func formatDirectoryEntry(dir string, entry os.DirEntry) (string, error) {
	name := entry.Name()
	quotedName := strconv.Quote(name)
	info, err := entry.Info()
	if err != nil {
		return "", fmt.Errorf("inspect entry %s: %w", quotedName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Show the link text exactly as stored; listing must not resolve or follow
		// child symlinks.
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("read symlink %s: %w", quotedName, err)
		}
		return "symlink\t" + quotedName + " -> " + strconv.Quote(target), nil
	}
	if info.IsDir() {
		return "dir\t" + strconv.Quote(name+"/"), nil
	}
	if info.Mode().IsRegular() {
		return "file\t" + quotedName, nil
	}
	return "other\t" + quotedName + " (mode " + info.Mode().String() + ")", nil
}
