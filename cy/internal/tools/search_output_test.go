package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchToolOutput(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "match.txt"), "needle\n")
	mustWriteFile(t, filepath.Join(root, "sub", "match.txt"), "needle scoped\n")
	mustWriteFile(t, filepath.Join(root, "unique.glob"), "path fixture\n")
	ws := mustWorkspace(t, root)

	tests := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{
			name: "grep root path",
			run: func() (string, error) {
				result, err := ws.grep(context.Background(), toolCall(`{"pattern":"^needle$","path":"match.txt"}`))
				return result.Content, err
			},
			want: "matches: 1\n\nmatch.txt:1:needle\n",
		},
		{
			name: "grep scoped directory",
			run: func() (string, error) {
				result, err := ws.grep(context.Background(), toolCall(`{"pattern":"scoped","path":"sub"}`))
				return result.Content, err
			},
			want: "matches: 1\n\nsub/match.txt:1:needle scoped\n",
		},
		{
			name: "grep no matches",
			run: func() (string, error) {
				result, err := ws.grep(context.Background(), toolCall(`{"pattern":"absent"}`))
				return result.Content, err
			},
			want: "no matches\n",
		},
		{
			name: "glob root directory",
			run: func() (string, error) {
				result, err := ws.glob(context.Background(), toolCall(`{"pattern":"unique.glob"}`))
				return result.Content, err
			},
			want: "paths: 1\n\n./unique.glob\n",
		},
		{
			name: "glob no paths",
			run: func() (string, error) {
				result, err := ws.glob(context.Background(), toolCall(`{"pattern":"**/*.md"}`))
				return result.Content, err
			},
			want: "no paths\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.run()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("output mismatch\n got: %q\nwant: %q", got, test.want)
			}
		})
	}
}

func TestGrepLongLineOutput(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 350) + "needle"
	mustWriteFile(t, filepath.Join(root, "long.txt"), line+"\n")
	ws := mustWorkspace(t, root)

	result, err := ws.grep(context.Background(), toolCall(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "matches: 1\n\n./long.txt:1:" + strings.Repeat("x", 300) + " [... omitted end of long line]\n"
	if result.Content != want {
		t.Fatalf("long-line output mismatch\n got: %q\nwant: %q", result.Content, want)
	}
}

func TestGrepResultLimit(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "many.txt"), strings.Repeat("needle\n", maxGrepMatches+1))
	ws := mustWorkspace(t, root)

	result, err := ws.grep(context.Background(), toolCall(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("matches: %d\ntruncated: true\n\n", maxGrepMatches)
	if !strings.HasPrefix(result.Content, prefix) {
		t.Fatalf("grep cap header = %q", result.Content)
	}
	if strings.Contains(result.Content, fmt.Sprintf("many.txt:%d:", maxGrepMatches+1)) {
		t.Fatalf("grep returned result beyond cap: %q", result.Content)
	}
}

func TestGlobResultLimit(t *testing.T) {
	root := t.TempDir()
	for index := range maxGlobMatches + 1 {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("file-%03d.go", index)), "package fixture\n")
	}
	ws := mustWorkspace(t, root)

	result, err := ws.glob(context.Background(), toolCall(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("paths: %d\ntruncated: true\n\n", maxGlobMatches)
	if !strings.HasPrefix(result.Content, prefix) {
		t.Fatalf("glob cap header = %q", result.Content)
	}
}

func TestGrepOutputByteLimit(t *testing.T) {
	root := t.TempDir()
	components := make([]string, 13)
	for index := range components {
		components[index] = strings.Repeat(string(rune('a'+index)), 210)
	}
	parts := append([]string{root}, components...)
	parts = append(parts, "many.txt")
	path := filepath.Join(parts...)
	mustWriteFile(t, path, strings.Repeat("needle\n", maxGrepMatches+1))
	ws := mustWorkspace(t, root)

	result, err := ws.grep(context.Background(), toolCall(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if _, err := fmt.Sscanf(result.Content, "matches: %d", &count); err != nil {
		t.Fatalf("parse match count: %v: %q", err, result.Content)
	}
	if count <= 0 || count >= maxGrepMatches {
		t.Fatalf("byte-capped match count = %d, want between 1 and %d", count, maxGrepMatches-1)
	}
	if !strings.Contains(result.Content, "\ntruncated: true\n") {
		t.Fatalf("byte-capped output is not marked truncated: %q", result.Content)
	}
}
