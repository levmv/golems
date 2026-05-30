package deploy

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bundled "github.com/levmv/golems/hugin/collectors"
	"github.com/levmv/golems/hugin/internal/config"
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

func TestArchiveBundledCollectorsIncludesExecutableScripts(t *testing.T) {
	archive, files, err := archiveBundledCollectors()
	if err != nil {
		t.Fatalf("archiveBundledCollectors returned error: %v", err)
	}
	if files != len(bundled.Files) {
		t.Fatalf("expected %d bundled files, got %d", len(bundled.Files), files)
	}

	entries := tarEntries(t, archive)
	if entries["collectors/disk"] == nil {
		t.Fatalf("archive missing collectors/disk; entries=%v", entryNames(entries))
	}
	if got := entries["collectors/disk"].Mode; got != 0755 {
		t.Fatalf("expected disk mode 0755, got %#o", got)
	}
	if got := entries["collectors/lib.sh"].Mode; got != 0644 {
		t.Fatalf("expected lib.sh mode 0644, got %#o", got)
	}
	if got := entries["collectors/hugin-collector-wrapper"].Mode; got != 0755 {
		t.Fatalf("expected wrapper mode 0755, got %#o", got)
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

	entries := tarEntries(t, archive)
	for _, name := range []string{"collectors", "collectors/lib.sh", "collectors/disk"} {
		if entries[name] == nil {
			t.Fatalf("archive missing %q; entries=%v", name, entryNames(entries))
		}
	}
}

func tarEntries(t *testing.T, archive io.Reader) map[string]*tar.Header {
	t.Helper()

	gz, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatalf("NewReader returned error: %v", err)
	}
	defer gz.Close()

	entries := map[string]*tar.Header{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read returned error: %v", err)
		}
		copy := *header
		entries[header.Name] = &copy
	}
	return entries
}

func entryNames(entries map[string]*tar.Header) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	return names
}

func TestShellQuote(t *testing.T) {
	got := shellQuote("/tmp/hugin's collectors")
	want := "'/tmp/hugin'\\''s collectors'"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestAuthorizedKeyLineUsesDefaultWrapperCommand(t *testing.T) {
	keyPath := writeTestPrivateKey(t)
	line, err := AuthorizedKeyLine(config.Target{Key: keyPath}, DefaultCollectorsDest, "hugin-web1")
	if err != nil {
		t.Fatalf("AuthorizedKeyLine returned error: %v", err)
	}
	if !strings.HasPrefix(line, `restrict,command="/opt/hugin/collectors/hugin-collector-wrapper" ssh-rsa `) {
		t.Fatalf("unexpected authorized_keys line: %s", line)
	}
	if !strings.HasSuffix(line, " hugin-web1") {
		t.Fatalf("expected sanitized comment suffix, got %s", line)
	}
}

func TestAuthorizedKeyLineUsesCollectorDirForCustomDest(t *testing.T) {
	keyPath := writeTestPrivateKey(t)
	line, err := AuthorizedKeyLine(config.Target{Key: keyPath}, "/home/hugin/collectors/", "hugin web1")
	if err != nil {
		t.Fatalf("AuthorizedKeyLine returned error: %v", err)
	}
	want := `restrict,command="HUGIN_COLLECTOR_DIR=/home/hugin/collectors /home/hugin/collectors/hugin-collector-wrapper" ssh-rsa `
	if !strings.HasPrefix(line, want) {
		t.Fatalf("expected prefix %q, got %s", want, line)
	}
	if !strings.HasSuffix(line, " hugin-web1") {
		t.Fatalf("expected whitespace in comment to be sanitized, got %s", line)
	}
}

func TestAuthorizedKeyLineRejectsNonAbsoluteDest(t *testing.T) {
	keyPath := writeTestPrivateKey(t)
	_, err := AuthorizedKeyLine(config.Target{Key: keyPath}, "~/hugin/collectors", "hugin")
	if err == nil {
		t.Fatal("expected AuthorizedKeyLine to reject non-absolute dest")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected absolute dest error, got %v", err)
	}
}

func TestAuthorizedKeyLineRejectsDestWithShellSpecials(t *testing.T) {
	keyPath := writeTestPrivateKey(t)
	_, err := AuthorizedKeyLine(config.Target{Key: keyPath}, "/tmp/hugin collectors", "hugin")
	if err == nil {
		t.Fatal("expected AuthorizedKeyLine to reject shell-special dest")
	}
	if !strings.Contains(err.Error(), "shell-special") {
		t.Fatalf("expected shell-special dest error, got %v", err)
	}
}

func TestSourceIncludesWrapperRequiresExecutableWrapper(t *testing.T) {
	dir := t.TempDir()
	if sourceIncludesWrapper(dir) {
		t.Fatal("expected empty source to not include wrapper")
	}
	path := filepath.Join(dir, "hugin-collector-wrapper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if sourceIncludesWrapper(dir) {
		t.Fatal("expected non-executable wrapper to not count as usable")
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("Chmod returned error: %v", err)
	}
	if !sourceIncludesWrapper(dir) {
		t.Fatal("expected executable wrapper to count as usable")
	}
}

func writeTestPrivateKey(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey returned error: %v", err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	path := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile key returned error: %v", err)
	}
	return path
}
