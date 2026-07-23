package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

const (
	defaultReadLines    = 200
	maxReadLines        = 2000
	maxReadOutputBytes  = 256 * 1024
	maxReadableFileSize = 64 * 1024 * 1024
	maxEditableFileSize = 8 * 1024 * 1024
	maxGrepMatches      = 100
	maxGlobMatches      = 500
	maxSearchOutput     = 256 * 1024
)

func (w *workspaceTools) read(_ context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	abs, display, err := w.resolveReadableFile(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := clampLimit(args.Limit, defaultReadLines, maxReadLines)
	content, next, more, digest, err := readNumberedLines(abs, offset, limit)
	if err != nil {
		return golem.ToolResult{}, err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "sha256: %s\n", digest)
	if more {
		fmt.Fprintf(&out, "continue: read(path=%q, offset=%d, limit=%d)\n", display, next, limit)
	}
	out.WriteByte('\n')
	if content == "" {
		out.WriteString("no lines\n")
	} else {
		out.WriteString(content)
	}
	return golem.ToolResult{Content: out.String()}, nil
}

func readNumberedLines(path string, offset, limit int) (content string, next int, more bool, digest string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", offset, false, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", offset, false, "", err
	}
	if info.Size() > maxReadableFileSize {
		return "", offset, false, "", fmt.Errorf("file exceeds %d-byte limit", maxReadableFileSize)
	}

	reader := bufio.NewReader(io.LimitReader(file, maxReadableFileSize+1))
	hash := sha256.New()
	var out strings.Builder
	lineNumber := 0
	returned := 0
	var bytesRead int64
	outputFull := false
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			bytesRead += int64(len(raw))
			if bytesRead > maxReadableFileSize {
				return "", offset, false, "", fmt.Errorf("file exceeds %d-byte limit", maxReadableFileSize)
			}
			_, _ = hash.Write(raw)
			if bytes.IndexByte(raw, 0) >= 0 {
				return "", offset, false, "", errors.New("file appears to be binary")
			}
			if !utf8.Valid(raw) {
				return "", offset, false, "", errors.New("file is not valid UTF-8")
			}

			lineNumber++
			if lineNumber >= offset && returned < limit && !outputFull {
				line := raw
				if line[len(line)-1] == '\n' {
					line = line[:len(line)-1]
				}
				prefix := fmt.Sprintf("%6d\t", lineNumber)
				if out.Len()+len(prefix)+len(line)+1 <= maxReadOutputBytes {
					out.WriteString(prefix)
					_, _ = out.Write(line)
					out.WriteByte('\n')
					returned++
				} else if returned == 0 {
					out.WriteString(truncateNumberedLine(lineNumber, line))
					returned++
					outputFull = true
				} else {
					outputFull = true
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", offset, false, "", readErr
		}
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	next = offset + returned
	more = next <= lineNumber
	return out.String(), next, more, digest, nil
}

func truncateNumberedLine(lineNumber int, line []byte) string {
	prefix := fmt.Sprintf("%6d\t", lineNumber)
	const suffix = "… [line truncated]\n"
	available := max(0, maxReadOutputBytes-len(prefix)-len(suffix))
	cut := min(len(line), available)
	for cut > 0 && !utf8.Valid(line[:cut]) {
		cut--
	}
	return prefix + string(line[:cut]) + suffix
}

func (w *workspaceTools) grep(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return golem.ToolResult{}, errors.New("pattern is required")
	}
	_, display, _, err := w.resolveExistingPath(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	lines, truncated, err := w.runGrep(ctx, args.Pattern, args.Include, display)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if len(lines) == 0 {
		return golem.ToolResult{Content: "no matches\n"}, nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "matches: %d\n", len(lines))
	if truncated {
		out.WriteString("truncated: true\n")
	}
	out.WriteByte('\n')
	out.WriteString(strings.Join(lines, "\n"))
	out.WriteByte('\n')
	return golem.ToolResult{Content: out.String()}, nil
}

func (w *workspaceTools) runGrep(ctx context.Context, pattern, include, display string) ([]string, bool, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil, false, errors.New("grep requires ripgrep (rg) in PATH")
	}
	args := []string{"--line-number", "--no-heading", "--color=never", "--with-filename", "--hidden", "--max-columns=300", "--max-columns-preview"}
	if strings.TrimSpace(include) != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, "--glob", "!**/.git/**")
	args = append(args, "--", pattern, display)
	command := exec.CommandContext(ctx, rg, args...)
	command.Dir = w.root
	command.Env = minimalToolEnv(w.root)
	var capture cappedCapture
	capture.limit = maxSearchOutput
	command.Stdout = &capture
	command.Stderr = &capture
	err = command.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, false, ctxErr
			}
			return nil, false, fmt.Errorf("ripgrep failed: %s", strings.TrimSpace(string(capture.data)))
		}
	}
	lines := nonEmptyLines(string(capture.data))
	truncated := capture.truncated || len(lines) > maxGrepMatches
	if len(lines) > maxGrepMatches {
		lines = lines[:maxGrepMatches]
	}
	return lines, truncated, nil
}

func (w *workspaceTools) glob(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return golem.ToolResult{}, errors.New("pattern is required")
	}
	_, display, info, err := w.resolveExistingPath(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if !info.IsDir() {
		return golem.ToolResult{}, fmt.Errorf("%s is not a directory", display)
	}
	paths, truncated, err := w.runGlob(ctx, args.Pattern, display)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if len(paths) == 0 {
		return golem.ToolResult{Content: "no paths\n"}, nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "paths: %d\n", len(paths))
	if truncated {
		out.WriteString("truncated: true\n")
	}
	out.WriteByte('\n')
	out.WriteString(strings.Join(paths, "\n"))
	out.WriteByte('\n')
	return golem.ToolResult{Content: out.String()}, nil
}

func (w *workspaceTools) runGlob(ctx context.Context, pattern, display string) ([]string, bool, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return nil, false, errors.New("glob requires ripgrep (rg) in PATH")
	}
	command := exec.CommandContext(ctx, rg, "--files", "--color=never", "--hidden", "--glob", pattern, "--glob", "!**/.git/**", "--", display)
	command.Dir = w.root
	command.Env = minimalToolEnv(w.root)
	var capture cappedCapture
	capture.limit = maxSearchOutput
	command.Stdout = &capture
	command.Stderr = &capture
	err = command.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, false, ctxErr
			}
			return nil, false, fmt.Errorf("ripgrep failed: %s", strings.TrimSpace(string(capture.data)))
		}
	}
	paths := nonEmptyLines(string(capture.data))
	truncated := capture.truncated || len(paths) > maxGlobMatches
	if len(paths) > maxGlobMatches {
		paths = paths[:maxGlobMatches]
	}
	return paths, truncated, nil
}

func (w *workspaceTools) edit(_ context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	if args.OldText == "" {
		return golem.ToolResult{}, errors.New("old_text must not be empty")
	}
	if !utf8.ValidString(args.OldText) || !utf8.ValidString(args.NewText) {
		return golem.ToolResult{}, errors.New("old_text and new_text must be valid UTF-8")
	}
	abs, display, err := w.resolveReadableFile(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	old, err := readBoundedTextFile(abs, maxEditableFileSize)
	if err != nil {
		return golem.ToolResult{}, err
	}
	oldHash := sha256.Sum256(old)
	count := bytes.Count(old, []byte(args.OldText))
	if count != 1 {
		return golem.ToolResult{}, fmt.Errorf("old_text occurs %d times in %s; reread and provide one exact occurrence", count, display)
	}
	updated := bytes.Replace(old, []byte(args.OldText), []byte(args.NewText), 1)
	info, err := os.Stat(abs)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if err := atomicReplace(abs, updated, info.Mode().Perm(), &oldHash, false); err != nil {
		return golem.ToolResult{}, err
	}
	newHash := sha256.Sum256(updated)
	change := buildFileChangeMeta(display, "edited", old, updated)
	return golem.ToolResult{Content: formatEditResult(newHash), Meta: change}, nil
}

func (w *workspaceTools) write(_ context.Context, call llm.ToolCall) (golem.ToolResult, error) {
	var args struct {
		Path           string `json:"path"`
		Content        string `json:"content"`
		ExpectedSHA256 string `json:"expected_sha256"`
	}
	if err := decodeToolArgs(call, &args); err != nil {
		return golem.ToolResult{}, err
	}
	if !utf8.ValidString(args.Content) {
		return golem.ToolResult{}, errors.New("content must be valid UTF-8")
	}
	if len(args.Content) > maxEditableFileSize {
		return golem.ToolResult{}, fmt.Errorf("content exceeds %d-byte write limit", maxEditableFileSize)
	}
	abs, display, exists, mode, old, oldHash, err := w.resolveWriteTarget(args.Path)
	if err != nil {
		return golem.ToolResult{}, err
	}
	if args.ExpectedSHA256 != "" {
		if !exists {
			return golem.ToolResult{}, errors.New("expected_sha256 was supplied but the file does not exist")
		}
		if err := verifyExpectedHash(args.ExpectedSHA256, oldHash); err != nil {
			return golem.ToolResult{}, err
		}
	}
	updated := []byte(args.Content)
	var expected *[32]byte
	if exists {
		expected = &oldHash
	}
	if err := atomicReplace(abs, updated, mode, expected, !exists); err != nil {
		return golem.ToolResult{}, err
	}
	newHash := sha256.Sum256(updated)
	operation := "created"
	if exists {
		operation = "replaced"
	}
	change := buildFileChangeMeta(display, operation, old, updated)
	return golem.ToolResult{Content: formatWriteResult(operation, newHash), Meta: change}, nil
}

func (w *workspaceTools) resolveWriteTarget(path string) (abs, display string, exists bool, mode os.FileMode, old []byte, digest [32]byte, err error) {
	abs, display, err = w.resolvePath(path)
	if err != nil {
		return
	}
	if display == "." {
		err = errors.New("file path is required")
		return
	}
	parent := filepath.Dir(abs)
	realParent, parentErr := ensureWritableParent(w.root, parent)
	if parentErr != nil {
		err = fmt.Errorf("prepare parent for %s: %w", display, parentErr)
		return
	}
	abs = filepath.Join(realParent, filepath.Base(abs))
	info, statErr := os.Lstat(abs)
	if errors.Is(statErr, os.ErrNotExist) {
		mode = 0o644
		return abs, display, false, mode, nil, digest, nil
	}
	if statErr != nil {
		err = statErr
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		var target string
		target, err = filepath.EvalSymlinks(abs)
		if err != nil {
			return
		}
		if !isWithinRoot(w.root, target) {
			err = fmt.Errorf("symlink target escapes workspace root: %s", display)
			return
		}
		abs = target
		info, err = os.Stat(abs)
		if err != nil {
			return
		}
	}
	if !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", display)
		return
	}
	old, err = readBoundedTextFile(abs, maxEditableFileSize)
	if err != nil {
		return
	}
	digest = sha256.Sum256(old)
	return abs, display, true, info.Mode().Perm(), old, digest, nil
}

func ensureWritableParent(root, parent string) (string, error) {
	probe := parent
	var missing []string
	for {
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", fmt.Errorf("%s is not a directory", probe)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(probe))
		next := filepath.Dir(probe)
		if next == probe {
			return "", errors.New("no existing parent directory")
		}
		probe = next
	}
	realParent, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	if !isWithinRoot(root, realParent) {
		return "", errors.New("parent symlink escapes workspace root")
	}
	for i := len(missing) - 1; i >= 0; i-- {
		next := filepath.Join(realParent, missing[i])
		if err := os.Mkdir(next, 0o755); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, statErr := os.Lstat(next)
			if statErr != nil {
				return "", statErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("parent path changed to a symlink while creating directories")
			}
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", next)
			}
		}
		realParent = next
	}
	return realParent, nil
}

func atomicReplace(path string, content []byte, mode os.FileMode, expected *[32]byte, requireAbsent bool) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".cy-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := temp.Name()
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return err
	}
	if n, err := temp.Write(content); err != nil || n != len(content) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if requireAbsent {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("file appeared while write was being prepared; reread before replacing it")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if expected != nil {
		current, err := readBoundedTextFile(path, maxEditableFileSize)
		if err != nil {
			return err
		}
		if currentHash := sha256.Sum256(current); currentHash != *expected {
			return errors.New("file changed while the update was being prepared; reread before retrying")
		}
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = dirHandle.Sync()
	_ = dirHandle.Close()
	if err != nil {
		return fmt.Errorf("sync file directory: %w", err)
	}
	ok = true
	return nil
}

func readBoundedTextFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d-byte limit", maxBytes)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("file appears to be binary")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("file is not valid UTF-8")
	}
	return data, nil
}

func verifyExpectedHash(raw string, actual [32]byte) error {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return nil
	}
	if len(raw) != 64 {
		return errors.New("expected_sha256 must contain 64 hexadecimal characters")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return errors.New("expected_sha256 must contain 64 hexadecimal characters")
	}
	if !bytes.Equal(decoded, actual[:]) {
		return fmt.Errorf("file hash is %s, not expected %s; reread before replacing", hex.EncodeToString(actual[:]), raw)
	}
	return nil
}

func formatEditResult(newHash [32]byte) string {
	return fmt.Sprintf("updated\nsha256: %x", newHash)
}

func formatWriteResult(operation string, newHash [32]byte) string {
	return fmt.Sprintf("%s\nsha256: %x", operation, newHash)
}

func nonEmptyLines(text string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), maxSearchOutput)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func minimalToolEnv(home string) []string {
	env := []string{"HOME=" + home}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type cappedCapture struct {
	data      []byte
	limit     int
	truncated bool
}

func (c *cappedCapture) Write(data []byte) (int, error) {
	original := len(data)
	remaining := c.limit - len(c.data)
	if remaining > 0 {
		c.data = append(c.data, data[:min(remaining, len(data))]...)
	}
	if len(data) > remaining {
		c.truncated = true
	}
	return original, nil
}
