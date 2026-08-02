package filesearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestFilesPositiveGlobsOverrideIgnores(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".git/secret.go", "package secret\n")
	writeTestFile(t, root, ".gitignore", "ignored.go\nignored-dir/\nnested/*.tmp\n")
	writeTestFile(t, root, ".config/settings.go", "package settings\n")
	writeTestFile(t, root, "ignored.go", "package ignored\n")
	writeTestFile(t, root, "ignored-dir/inside.go", "package inside\n")
	writeTestFile(t, root, "nested/drop.tmp", "ignored tmp\n")
	writeTestFile(t, root, "visible.go", "package visible\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		query FilesQuery
		want  []string
	}{
		{
			name:  "matching ignored file",
			query: FilesQuery{Glob: "*.go"},
			want:  []string{".config/settings.go", "ignored.go", "visible.go"},
		},
		{
			name:  "wide glob reopens ignored directory",
			query: FilesQuery{Glob: "*"},
			want:  []string{".config/settings.go", ".gitignore", "ignored-dir/inside.go", "ignored.go", "nested/drop.tmp", "visible.go"},
		},
		{
			name:  "scoped glob reopens ignored file",
			query: FilesQuery{Path: "nested", Glob: "*.tmp"},
			want:  []string{"nested/drop.tmp"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := collectFiles(t, searcher, test.query)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("paths = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFilesIgnoresAreRootLocal(t *testing.T) {
	parent := t.TempDir()
	writeTestFile(t, parent, ".gitignore", "*.go\n")
	writeTestFile(t, parent, "workspace/visible.go", "package visible\n")
	searcher, err := New(filepath.Join(parent, "workspace"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.tmp"})
	if want := []string{"visible.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestFilesSkipsLinkedWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".git", "gitdir: ../metadata/worktrees/example\n")
	writeTestFile(t, root, ".gitignore", "ignored.txt\n")
	writeTestFile(t, root, "ignored.txt", "ignored\n")
	writeTestFile(t, root, "visible.go", "package visible\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.tmp"})
	want := []string{".gitignore", "visible.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestFilesDoesNotFollowIgnoreFileSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, outside, ".gitignore", "visible.go\n")
	writeTestFile(t, outside, "exclude", "visible.go\n")
	if err := os.Symlink(filepath.Join(outside, ".gitignore"), filepath.Join(root, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".git", "info")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "visible.go", "package visible\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.tmp"})
	if want := []string{"visible.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestFilesIgnoreNoneStillExcludesGitMetadata(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".gitignore", "ignored.txt\n")
	writeTestFile(t, root, "ignored.txt", "visible when ignores are disabled\n")
	writeTestFile(t, root, ".git/secret.txt", "never visible\n")
	searcher, err := New(root, Options{Ignore: IgnoreNone})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.tmp"})
	want := []string{".gitignore", "ignored.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}

	got = collectFiles(t, searcher, FilesQuery{Path: ".git", Glob: "*"})
	if len(got) != 0 {
		t.Fatalf("explicit .git query returned %q", got)
	}
}

func TestFilesIgnoreSourcePrecedence(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".git/info/exclude", "info.txt\n")
	writeTestFile(t, root, ".gitignore", "*.txt\n!info.txt\n")
	writeTestFile(t, root, ".ignore", "!dot.txt\n!rg.txt\n")
	writeTestFile(t, root, ".rgignore", "rg.txt\n!nested/keep.txt\n")
	writeTestFile(t, root, "dot.txt", "kept by .ignore\n")
	writeTestFile(t, root, "info.txt", "kept by .gitignore\n")
	writeTestFile(t, root, "other.txt", "ignored by .gitignore\n")
	writeTestFile(t, root, "rg.txt", "ignored again by .rgignore\n")
	writeTestFile(t, root, "nested/.gitignore", "keep.txt\n")
	writeTestFile(t, root, "nested/keep.txt", "root .rgignore wins over nested .gitignore\n")

	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.tmp"})
	want := []string{
		".gitignore",
		".ignore",
		".rgignore",
		"dot.txt",
		"info.txt",
		"nested/.gitignore",
		"nested/keep.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestFilesIgnoreBraceAlternativesAndCRLF(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".gitignore", "*.{log,tmp}\r\n!keep.log\r\n# unmatched { in a comment\r\n")
	writeTestFile(t, root, "drop.log", "ignored\n")
	writeTestFile(t, root, "drop.tmp", "ignored\n")
	writeTestFile(t, root, "keep.log", "re-included\n")
	writeTestFile(t, root, "visible.go", "visible\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "!*.md"})
	want := []string{".gitignore", "keep.log", "visible.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestFilesStopAndCallbackError(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		writeTestFile(t, root, name, name)
	}
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	stats, err := searcher.Files(context.Background(), FilesQuery{Glob: "*.go"}, func(File) error {
		calls++
		if calls == 2 {
			return errors.Join(errors.New("limit reached"), ErrStop)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || stats.Results != 1 || !stats.Stopped {
		t.Fatalf("calls=%d stats=%+v", calls, stats)
	}

	wantErr := errors.New("sink failed")
	stats, err = searcher.Files(context.Background(), FilesQuery{Glob: "*.go"}, func(File) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) || stats.Results != 0 || stats.Stopped {
		t.Fatalf("error=%v stats=%+v", err, stats)
	}
}

func TestFilesUsesGlobalLexicalPathOrder(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a/inside.go", "package inside\n")
	writeTestFile(t, root, "a.go", "package a\n")
	writeTestFile(t, root, "a0.go", "package a0\n")
	searcher, err := New(root, Options{Ignore: IgnoreNone})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "*.go"})
	want := []string{"a.go", "a/inside.go", "a0.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want global lexical order %q", got, want)
	}
}

func TestFilesDirectGlobDoesNotInheritDirectoryMatches(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "foo/inside.txt", "not selected by the literal foo glob\n")
	writeTestFile(t, root, "other/foo", "selected basename\n")
	writeTestFile(t, root, "other/keep.txt", "ordinary file\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := collectFiles(t, searcher, FilesQuery{Glob: "foo"})
	if want := []string{"other/foo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("literal glob paths = %q, want %q", got, want)
	}
	got = collectFiles(t, searcher, FilesQuery{Glob: "foo/**"})
	if want := []string{"foo/inside.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descendant glob paths = %q, want %q", got, want)
	}
	got = collectFiles(t, searcher, FilesQuery{Glob: "!foo"})
	if want := []string{"other/keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("negative directory glob paths = %q, want %q", got, want)
	}
}

func TestFilesUnicodeGlob(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "é.go", "日本.go"} {
		writeTestFile(t, root, name, name)
	}
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pattern string
		want    []string
	}{
		{pattern: "?.go", want: []string{"a.go"}},
		{pattern: "??.go", want: []string{"é.go"}},
		{pattern: "*.go", want: []string{"a.go", "é.go", "日本.go"}},
	}
	for _, test := range cases {
		t.Run(test.pattern, func(t *testing.T) {
			got := collectFiles(t, searcher, FilesQuery{Glob: test.pattern})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("paths = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFilesCancellation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "visible.go", "package visible\n")
	writeTestFile(t, root, "z.go", "package z\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err = searcher.Files(ctx, FilesQuery{Glob: "*"}, func(File) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("error=%v called=%t", err, called)
	}

	ctx, cancel = context.WithCancel(context.Background())
	stats, err := searcher.Files(ctx, FilesQuery{Glob: "*"}, func(File) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || stats.Results != 1 {
		t.Fatalf("mid-walk cancellation error=%v stats=%+v", err, stats)
	}
}

func TestFilesValidatesQueries(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "content\n")
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []FilesQuery{
		{Glob: ""},
		{Glob: "!"},
		{Glob: "foo{bar,baz"},
		{Path: "../outside", Glob: "*"},
		{Path: filepath.Join(root, "file.txt"), Glob: "*"},
		{Path: "file.txt", Glob: "*"},
	}
	for _, query := range tests {
		if _, err := searcher.Files(context.Background(), query, func(File) error { return nil }); err == nil {
			t.Errorf("Files(%+v) unexpectedly succeeded", query)
		}
	}
	if _, err := searcher.Files(context.Background(), FilesQuery{Glob: "*"}, nil); err == nil {
		t.Error("nil callback unexpectedly succeeded")
	}
	if _, err := New(root, Options{Ignore: IgnoreMode(99)}); err == nil {
		t.Error("invalid IgnoreMode unexpectedly succeeded")
	}
}

func TestFilesDoesNotFollowDiscoveredSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, root, "real/in-root.go", "package real\n")
	writeTestFile(t, outside, "outside.go", "package outside\n")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "linked-file.go")); err != nil {
		t.Fatal(err)
	}
	searcher, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := collectFiles(t, searcher, FilesQuery{Glob: "*.go"})
	if want := []string{"real/in-root.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}

	// An explicitly named in-root directory symlink is allowed, but a symlink
	// that resolves outside the root is rejected by the rooted path policy.
	got = collectFiles(t, searcher, FilesQuery{Path: "linked-dir", Glob: "*.go"})
	if want := []string{"linked-dir/in-root.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit symlink paths = %q, want %q", got, want)
	}
	if _, err := searcher.Files(context.Background(), FilesQuery{Path: "linked-file.go", Glob: "*"}, func(File) error { return nil }); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("outside symlink error = %v", err)
	}
}

func TestExpandBraces(t *testing.T) {
	got, err := expandBraces("**/*.{go,txt}")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"**/*.go", "**/*.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expansion = %q, want %q", got, want)
	}
	if _, err := expandBraces("{a,b"); err == nil {
		t.Error("unmatched brace unexpectedly succeeded")
	}
	if _, err := expandBraces("{,a}"); err == nil {
		t.Error("empty alternative unexpectedly succeeded")
	}
}

func collectFiles(t *testing.T, searcher *Searcher, query FilesQuery) []string {
	t.Helper()
	var paths []string
	if _, err := searcher.Files(context.Background(), query, func(file File) error {
		paths = append(paths, file.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}

func writeTestFile(t *testing.T, root, path, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
