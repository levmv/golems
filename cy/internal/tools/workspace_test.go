package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func TestWorkspaceToolsRegisterExpectedTools(t *testing.T) {
	tools, root, err := NewWorkspaceTools(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspaceTools() error = %v", err)
	}
	if root == "" {
		t.Fatal("root is empty")
	}

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Definition.Function.Name)
		if tool.Definition.Function.Name == "read" || tool.Definition.Function.Name == lsToolName || tool.Definition.Function.Name == "grep" || tool.Definition.Function.Name == "glob" {
			if tool.Effect != golem.ToolEffectRead {
				t.Fatalf("tool %s effect = %q, want read", tool.Definition.Function.Name, tool.Effect)
			}
		} else if tool.Effect != golem.ToolEffectWrite {
			t.Fatalf("tool %s effect = %q, want write", tool.Definition.Function.Name, tool.Effect)
		}
	}
	want := []string{"read", "ls", "grep", "glob", "edit", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool names = %v, want %v", names, want)
	}
}

func TestReadReturnsNumberedRangeAndHash(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "docs", "note.txt"), "one\ntwo\nthree\n")
	ws := mustWorkspace(t, root)

	result, err := ws.read(context.Background(), toolCall(`{"path":"docs/note.txt","offset":2,"limit":1}`))
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if !strings.Contains(result.Content, "     2\ttwo") || strings.Contains(result.Content, "path:") {
		t.Fatalf("read() content = %q", result.Content)
	}
	if !strings.Contains(result.Content, "offset=3") {
		t.Fatalf("read() continuation = %q", result.Content)
	}
	if !regexp.MustCompile(`(?m)^sha256: [0-9a-f]{64}$`).MatchString(result.Content) {
		t.Fatalf("read() hash missing: %q", result.Content)
	}
}

func TestLSShowsUnfilteredEntryTypesAndSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWriteFile(t, filepath.Join(root, ".hidden"), "hidden\n")
	mustWriteFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	mustWriteFile(t, filepath.Join(root, "ignored", "backing.go"), "package ignored\n")
	mustWriteFile(t, filepath.Join(root, "visible.go"), "package visible\n")
	if err := os.Symlink("ignored", filepath.Join(root, "vendor-current")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Skipf("outside symlink unavailable: %v", err)
	}
	ws := mustWorkspace(t, root)
	result, err := ws.ls(context.Background(), toolCall(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`dir\t".git/"`,
		`file\t".gitignore"`,
		`file\t".hidden"`,
		`dir\t"ignored/"`,
		`symlink\t"outside-link" -> ` + strconv.Quote(outside),
		`symlink\t"vendor-current" -> "ignored"`,
		`file\t"visible.go"`,
	} {
		if !strings.Contains(result.Content, strings.ReplaceAll(want, `\t`, "\t")) {
			t.Errorf("ls result missed %q:\n%s", want, result.Content)
		}
	}
	if strings.Contains(result.Content, "backing.go") {
		t.Fatalf("ls recursed into ignored directory or symlink:\n%s", result.Content)
	}
}

func TestLSAllowsExplicitInRootSymlinkAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "backing", "target.go"), "package target\n")
	if err := os.Symlink("backing", filepath.Join(root, "current")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("outside symlink unavailable: %v", err)
	}
	ws := mustWorkspace(t, root)
	result, err := ws.ls(context.Background(), toolCall(`{"path":"current"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "file\t\"target.go\"") {
		t.Fatalf("explicit in-root directory symlink result:\n%s", result.Content)
	}
	if _, err := ws.ls(context.Background(), toolCall(`{"path":"outside"}`)); err == nil || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("escaping directory symlink error = %v", err)
	}
}

func TestLSPaginatesDeterministically(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		mustWriteFile(t, filepath.Join(root, name), name+"\n")
	}
	ws := mustWorkspace(t, root)
	result, err := ws.ls(context.Background(), toolCall(`{"offset":2,"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "entries: 1") || !strings.Contains(result.Content, "truncated: true") || !strings.Contains(result.Content, `offset=3`) || !strings.Contains(result.Content, "file\t\"b.go\"") {
		t.Fatalf("paginated ls result:\n%s", result.Content)
	}
	if strings.Contains(result.Content, `"a.go"`) || strings.Contains(result.Content, `"c.go"`) {
		t.Fatalf("paginated ls leaked another page:\n%s", result.Content)
	}
}

func TestSearchToolDescriptionsStateVisibilityAndEntryPolicy(t *testing.T) {
	tools, _, err := NewWorkspaceTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string)
	for _, tool := range tools {
		descriptions[tool.Definition.Function.Name] = tool.Definition.Function.Description
	}
	for _, term := range []string{"hidden", ".git", "binary", "invalid UTF-8", "symlink"} {
		if !strings.Contains(descriptions["grep"], term) {
			t.Errorf("grep description does not state %q policy: %q", term, descriptions["grep"])
		}
	}
	for _, term := range []string{"hidden", "regular files", "symlink", ".git"} {
		if !strings.Contains(descriptions["glob"], term) {
			t.Errorf("glob description does not state %q policy: %q", term, descriptions["glob"])
		}
	}
	for _, term := range []string{"without ignore filtering", "hidden", ".git", "symlink targets", "does not follow"} {
		if !strings.Contains(descriptions[lsToolName], term) {
			t.Errorf("ls description does not state %q policy: %q", term, descriptions[lsToolName])
		}
	}
}

func TestReadCanRangeBeyondEditableFileLimit(t *testing.T) {
	root := t.TempDir()
	const prefixLines = 1_100_000
	path := filepath.Join(root, "large.txt")
	mustWriteFile(t, path, strings.Repeat("padding\n", prefixLines)+"target\n")
	ws := mustWorkspace(t, root)

	result, err := ws.read(context.Background(), toolCall(`{"path":"large.txt","offset":1100001,"limit":1}`))
	if err != nil {
		t.Fatalf("read() large range error = %v", err)
	}
	if !strings.Contains(result.Content, "1100001\ttarget") {
		t.Fatalf("read() large range content = %q", result.Content)
	}
}

func TestReadTruncatesOversizedLineAndAdvances(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "long.txt"), strings.Repeat("x", maxReadOutputBytes+100)+"\nnext\n")
	ws := mustWorkspace(t, root)

	result, err := ws.read(context.Background(), toolCall(`{"path":"long.txt","limit":1}`))
	if err != nil {
		t.Fatalf("read() error = %v", err)
	}
	if !strings.Contains(result.Content, "[line truncated]") || !strings.Contains(result.Content, "offset=2") {
		t.Fatalf("read() oversized line result = %q", result.Content)
	}
}

func TestWorkspaceToolsRejectPathEscape(t *testing.T) {
	ws := mustWorkspace(t, t.TempDir())
	if _, _, err := ws.resolvePath("../outside.txt"); err == nil {
		t.Fatal("resolvePath() accepted parent escape")
	}
	if _, _, err := ws.resolvePath(filepath.Clean(os.TempDir())); err == nil {
		t.Fatal("resolvePath() accepted absolute path")
	}
}

func TestWorkspaceToolsRejectSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outside, "secret\n")
	link := filepath.Join(root, "outside-link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ws := mustWorkspace(t, root)

	if _, err := ws.read(context.Background(), toolCall(`{"path":"outside-link.txt"}`)); err == nil {
		t.Fatal("read() accepted symlink escape")
	}
	if _, err := ws.grep(context.Background(), toolCall(`{"pattern":"secret","path":"outside-link.txt"}`)); err == nil {
		t.Fatal("grep() accepted symlink escape")
	}

	outsideDir := t.TempDir()
	mustWriteFile(t, filepath.Join(outsideDir, "nested.txt"), "secret\n")
	if err := os.Symlink(outsideDir, filepath.Join(root, "outside-dir")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := ws.read(context.Background(), toolCall(`{"path":"outside-dir/nested.txt"}`)); err == nil {
		t.Fatal("read() accepted escape through an intermediate symlink")
	}
	if _, err := ws.edit(context.Background(), toolCall(`{"path":"outside-dir/nested.txt","old_text":"secret","new_text":"changed"}`)); err == nil {
		t.Fatal("edit() accepted escape through an intermediate symlink")
	}
}

func TestGrepFindsRegexMatches(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), "Alpha\nbeta ALPHA\n")
	mustWriteFile(t, filepath.Join(root, "b.txt"), "nothing\n")
	ws := mustWorkspace(t, root)

	result, err := ws.grep(context.Background(), toolCall(`{"pattern":"(?i)alpha","include":"*.txt"}`))
	if err != nil {
		t.Fatalf("grep() error = %v", err)
	}
	if !strings.Contains(result.Content, "a.txt:1:Alpha") || !strings.Contains(result.Content, "a.txt:2:beta ALPHA") {
		t.Fatalf("grep() content = %q", result.Content)
	}
	if strings.Contains(result.Content, "b.txt") {
		t.Fatalf("grep() included non-match: %q", result.Content)
	}
}

func TestGlobHonorsIgnores(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".git", "hidden.go"), "ignored\n")
	mustWriteFile(t, filepath.Join(root, ".github", "workflow.go"), "workflow\n")
	mustWriteFile(t, filepath.Join(root, "pkg", "visible.go"), "visible\n")
	ws := mustWorkspace(t, root)

	result, err := ws.glob(context.Background(), toolCall(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatalf("glob() error = %v", err)
	}
	if !strings.Contains(result.Content, "pkg/visible.go") || !strings.Contains(result.Content, ".github/workflow.go") {
		t.Fatalf("glob() missed visible or hidden workspace file: %q", result.Content)
	}
	if strings.Contains(result.Content, ".git/") {
		t.Fatalf("glob() included .git: %q", result.Content)
	}
}

func TestGrepSearchesHiddenWorkspaceFilesButSkipsGitMetadata(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, ".git", "metadata.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, ".config", "settings.txt"), "needle\n")
	ws := mustWorkspace(t, root)
	result, err := ws.grep(context.Background(), toolCall(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, ".config/settings.txt") || strings.Contains(result.Content, ".git/") {
		t.Fatalf("grep hidden-file policy = %q", result.Content)
	}
}

func TestSearchToolsWorkWithEmptyPATH(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "visible.txt"), "text\n")
	ws := mustWorkspace(t, root)
	t.Setenv("PATH", "")
	grep, err := ws.grep(context.Background(), toolCall(`{"pattern":"text"}`))
	if err != nil || !strings.Contains(grep.Content, "visible.txt:1:text") {
		t.Fatalf("grep() result = %q, error = %v", grep.Content, err)
	}
	glob, err := ws.glob(context.Background(), toolCall(`{"pattern":"**/*.txt"}`))
	if err != nil || !strings.Contains(glob.Content, "visible.txt") {
		t.Fatalf("glob() result = %q, error = %v", glob.Content, err)
	}
}

func TestEditRequiresUniqueTextAndReturnsStructuredChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	mustWriteFile(t, path, "alpha\nbeta\n")
	ws := mustWorkspace(t, root)

	result, err := ws.edit(context.Background(), toolCall(`{"path":"note.txt","old_text":"beta","new_text":"gamma"}`))
	if err != nil {
		t.Fatalf("edit() error = %v", err)
	}
	if !regexp.MustCompile(`^updated\nsha256: [0-9a-f]{64}$`).MatchString(result.Content) {
		t.Fatalf("edit() summary = %q", result.Content)
	}
	change, ok := fileChangeMetaFrom(result.Meta)
	if !ok || change.Path != "note.txt" || change.Operation != "edited" || change.Additions != 1 || change.Deletions != 1 {
		t.Fatalf("edit() meta = %#v, ok=%v", result.Meta, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\ngamma\n" {
		t.Fatalf("edited file = %q", data)
	}
	if _, err := ws.edit(context.Background(), toolCall(`{"path":"note.txt","old_text":"a","new_text":"x"}`)); err == nil || !strings.Contains(err.Error(), "occurs") {
		t.Fatalf("ambiguous edit error = %v", err)
	}
}

func TestWriteCreatesParentsAtomically(t *testing.T) {
	root := t.TempDir()
	ws := mustWorkspace(t, root)
	result, err := ws.write(context.Background(), toolCall(`{"path":"new/nested/file.txt","content":"hello\n"}`))
	if err != nil {
		t.Fatalf("write() error = %v", err)
	}
	if !regexp.MustCompile(`^created\nsha256: [0-9a-f]{64}$`).MatchString(result.Content) {
		t.Fatalf("write() result = %q", result.Content)
	}
	change, ok := fileChangeMetaFrom(result.Meta)
	if !ok || change.Operation != "created" || change.Additions != 1 || change.Deletions != 0 {
		t.Fatalf("write() meta = %#v, ok=%v", result.Meta, ok)
	}
	data, err := os.ReadFile(filepath.Join(root, "new", "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("written file = %q", data)
	}
}

func TestWriteDoesNotCreateThroughEscapingParentSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	ws := mustWorkspace(t, root)
	if _, err := ws.write(context.Background(), toolCall(`{"path":"escape/new/file.txt","content":"no"}`)); err == nil {
		t.Fatal("write() accepted escaping parent symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write() created outside directory, stat error = %v", err)
	}
}

func toolCall(args string) llm.ToolCall {
	return llm.ToolCall{Function: llm.ToolFunction{Arguments: args}}
}

func mustWorkspace(t *testing.T, root string) *workspaceTools {
	t.Helper()
	ws, err := newWorkspaceToolset(root)
	if err != nil {
		t.Fatalf("newWorkspaceToolset() error = %v", err)
	}
	return ws
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
