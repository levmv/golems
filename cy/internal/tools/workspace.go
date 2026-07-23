package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

type workspaceTools struct {
	root string
}

// NewWorkspaceTools builds the file and search tools rooted at a canonical
// workspace directory. The returned root is resolved and symlink-free.
func NewWorkspaceTools(root string) ([]golem.Tool, string, error) {
	ws, err := newWorkspaceToolset(root)
	if err != nil {
		return nil, "", err
	}
	return []golem.Tool{
		golem.FunctionToolWithEffect(
			golem.ToolEffectRead,
			"read",
			"Read a UTF-8 text file with stable line numbers. Use offset and limit to continue large files.",
			jsonschema.Obj(
				jsonschema.Required("path", jsonschema.Str{Description: "File path relative to the workspace root."}),
				jsonschema.Optional("offset", jsonschema.Int{
					Description: "One-based first line to return. Defaults to 1.",
					Minimum:     new(1),
				}),
				jsonschema.Optional("limit", jsonschema.Int{
					Description: "Maximum lines to return. Defaults to 200; capped at 2000.",
					Default:     defaultReadLines,
					Minimum:     new(1),
					Maximum:     new(maxReadLines),
				}),
			).NoAdditionalProperties(),
			ws.read,
		),
		golem.FunctionToolWithEffect(
			golem.ToolEffectRead,
			"grep",
			"Search UTF-8 repository text with a regular expression. Honors repository ignore files and caps results.",
			jsonschema.Obj(
				jsonschema.Required("pattern", jsonschema.Str{Description: "Regular expression to search for."}),
				jsonschema.Optional("path", jsonschema.Str{Description: "File or directory relative to the workspace root. Defaults to the root."}),
				jsonschema.Optional("include", jsonschema.Str{Description: "Optional ripgrep glob such as *.go or **/*_test.go."}),
			).NoAdditionalProperties(),
			ws.grep,
		),
		golem.FunctionToolWithEffect(
			golem.ToolEffectRead,
			"glob",
			"Find repository paths matching a glob. Honors repository ignore files and caps results.",
			jsonschema.Obj(
				jsonschema.Required("pattern", jsonschema.Str{Description: "Glob such as **/*.go."}),
				jsonschema.Optional("path", jsonschema.Str{Description: "Directory relative to the workspace root. Defaults to the root."}),
			).NoAdditionalProperties(),
			ws.glob,
		),
		golem.FunctionToolWithEffect(
			golem.ToolEffectWrite,
			"edit",
			"Replace one exact, unique text occurrence in a UTF-8 file atomically. Fails on ambiguous matches.",
			jsonschema.Obj(
				jsonschema.Required("path", jsonschema.Str{Description: "File path relative to the workspace root."}),
				jsonschema.Required("old_text", jsonschema.Str{Description: "Exact text that must occur exactly once."}),
				jsonschema.Required("new_text", jsonschema.Str{Description: "Replacement text; may be empty."}),
			).NoAdditionalProperties(),
			ws.edit,
		),
		golem.FunctionToolWithEffect(
			golem.ToolEffectWrite,
			"write",
			"Create or replace one complete UTF-8 file atomically. Fails if an optional expected hash is stale.",
			jsonschema.Obj(
				jsonschema.Required("path", jsonschema.Str{Description: "File path relative to the workspace root."}),
				jsonschema.Required("content", jsonschema.Str{Description: "Complete new file content."}),
				jsonschema.Optional("expected_sha256", jsonschema.Str{Description: "Optional SHA-256 of the existing file."}),
			).NoAdditionalProperties(),
			ws.write,
		),
	}, ws.root, nil
}

func newWorkspaceToolset(root string) (*workspaceTools, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", abs)
	}
	return &workspaceTools{root: abs}, nil
}

func (w *workspaceTools) resolvePath(path string) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	if filepath.IsAbs(path) {
		return "", "", fmt.Errorf("absolute paths are not allowed: %s", path)
	}
	cleaned := filepath.Clean(path)
	abs := filepath.Clean(filepath.Join(w.root, cleaned))
	if !isWithinRoot(w.root, abs) {
		return "", "", fmt.Errorf("path escapes workspace root: %s", path)
	}
	return abs, w.displayPath(abs), nil
}

func (w *workspaceTools) resolveExistingPath(path string) (string, string, os.FileInfo, error) {
	abs, display, err := w.resolvePath(path)
	if err != nil {
		return "", "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", nil, err
	}
	if !isWithinRoot(w.root, resolved) {
		return "", "", nil, fmt.Errorf("symlink target escapes workspace root: %s", display)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", "", nil, fmt.Errorf("%s is not a regular file or directory", display)
	}
	return resolved, display, info, nil
}

func (w *workspaceTools) resolveReadableFile(path string) (string, string, error) {
	abs, display, info, err := w.resolveExistingPath(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("%s is a directory", display)
	}
	return abs, display, nil
}

func (w *workspaceTools) displayPath(path string) string {
	rel, err := filepath.Rel(w.root, path)
	if err != nil || rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

func decodeToolArgs(call llm.ToolCall, target any) error {
	args := strings.TrimSpace(call.Function.Arguments)
	if args == "" {
		args = "{}"
	}
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid tool arguments: multiple JSON values")
	}
	return nil
}

func clampLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	return min(value, maximum)
}

func isWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
