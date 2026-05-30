package deploy

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCollectorsSourceUsesExplicitDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.sh"), []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	got, err := ResolveCollectorsSource(dir)
	if err != nil {
		t.Fatalf("ResolveCollectorsSource returned error: %v", err)
	}
	if got != dir {
		t.Fatalf("expected %q, got %q", dir, got)
	}
}

func TestArchiveCollectorsPreservesTopLevelDirectory(t *testing.T) {
	dir := t.TempDir()
	collectors := filepath.Join(dir, "collectors")
	if err := os.Mkdir(collectors, 0755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(collectors, "lib.sh"), []byte("lib"), 0644); err != nil {
		t.Fatalf("WriteFile lib returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(collectors, "disk"), []byte("disk"), 0755); err != nil {
		t.Fatalf("WriteFile disk returned error: %v", err)
	}

	archive, files, err := archiveCollectors(collectors)
	if err != nil {
		t.Fatalf("archiveCollectors returned error: %v", err)
	}
	if files != 2 {
		t.Fatalf("expected 2 regular files, got %d", files)
	}

	gz, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read returned error: %v", err)
		}
		seen[header.Name] = true
	}
	for _, name := range []string{"collectors", "collectors/lib.sh", "collectors/disk"} {
		if !seen[name] {
			t.Fatalf("archive missing %q; seen=%v", name, seen)
		}
	}
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("/tmp/hugin's collectors")
	want := "'/tmp/hugin'\\''s collectors'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
