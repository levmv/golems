package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxInstructionBytes = 1024 * 1024

// loadInstructions reads the instructions that apply to the current working
// directory. Sessions intentionally do not preserve historical copies: a new
// process or resume uses the repository's current instructions.
func loadInstructions(root string) ([]string, error) {
	candidates, err := instructionCandidates(root)
	if err != nil {
		return nil, err
	}
	prompts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		content, found, err := readInstruction(root, candidate)
		if err != nil {
			return nil, err
		}
		if found {
			prompts = append(prompts, fmt.Sprintf("Instructions from %s:\n%s", candidate, content))
		}
	}
	return prompts, nil
}

func instructionCandidates(root string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve instruction root: %w", err)
	}
	working, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve working directory for instructions: %w", err)
	}
	rel, err := filepath.Rel(root, working)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = "."
	}

	candidates := []string{"AGENTS.md"}
	if rel == "." {
		return candidates, nil
	}
	dir := ""
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		candidates = append(candidates, filepath.ToSlash(filepath.Join(dir, "AGENTS.md")))
	}
	return candidates, nil
}

func readInstruction(root, relative string) (string, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve instruction %s: %w", relative, err)
	}
	inside, err := filepath.Rel(root, resolved)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("instruction %s resolves outside the workspace", relative)
	}
	path = resolved
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("inspect resolved instruction %s: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("instruction %s is not a regular file", relative)
	}
	if info.Size() > maxInstructionBytes {
		return "", false, fmt.Errorf("instruction %s is larger than %d bytes", relative, maxInstructionBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("read instruction %s: %w", relative, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxInstructionBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("read instruction %s: %w", relative, err)
	}
	if len(content) > maxInstructionBytes {
		return "", false, fmt.Errorf("instruction %s is larger than %d bytes", relative, maxInstructionBytes)
	}
	return string(content), true, nil
}
