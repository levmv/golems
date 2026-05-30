package collectors

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorWrapperAllowsBundledCollectorCommand(t *testing.T) {
	dir := t.TempDir()
	collectorPath := filepath.Join(dir, "disk")
	if err := os.WriteFile(collectorPath, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$HUGIN_CHECK_ID:$HUGIN_DISK_PATH\"\n"), 0755); err != nil {
		t.Fatalf("WriteFile collector returned error: %v", err)
	}

	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+dir+"/",
		"SSH_ORIGINAL_COMMAND=HUGIN_CHECK_ID=disk_web1  HUGIN_DISK_PATH=/\t"+collectorPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper returned error: %v output=%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "disk_web1:/" {
		t.Fatalf("unexpected wrapper output %q", got)
	}
}

func TestCollectorWrapperRejectsNonHuginEnvAssignment(t *testing.T) {
	dir := t.TempDir()
	collectorPath := filepath.Join(dir, "disk")
	if err := os.WriteFile(collectorPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile collector returned error: %v", err)
	}

	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+dir,
		"SSH_ORIGINAL_COMMAND=PATH=/tmp "+collectorPath,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject non-HUGIN env, output=%s", out)
	}
	if !strings.Contains(string(out), "only HUGIN_*") {
		t.Fatalf("expected non-HUGIN env error, got %s", out)
	}
}

func TestCollectorWrapperRejectsMalformedHuginEnvName(t *testing.T) {
	dir := t.TempDir()
	collectorPath := filepath.Join(dir, "disk")
	if err := os.WriteFile(collectorPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile collector returned error: %v", err)
	}

	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+dir,
		"SSH_ORIGINAL_COMMAND=HUGIN_BAD-NAME=value "+collectorPath,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject malformed HUGIN env, output=%s", out)
	}
	if !strings.Contains(string(out), "only HUGIN_*") {
		t.Fatalf("expected HUGIN env error, got %s", out)
	}
}

func TestCollectorWrapperRejectsShellMetacharacters(t *testing.T) {
	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+t.TempDir(),
		"SSH_ORIGINAL_COMMAND=HUGIN_CHECK_ID=x /opt/hugin/collectors/disk; uname -a",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject metacharacters, output=%s", out)
	}
	if !strings.Contains(string(out), "unsupported characters") {
		t.Fatalf("expected unsupported characters error, got %s", out)
	}
}

func TestCollectorWrapperRejectsMultilineCommand(t *testing.T) {
	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+t.TempDir(),
		"SSH_ORIGINAL_COMMAND=HUGIN_CHECK_ID=x /opt/hugin/collectors/disk\n/opt/hugin/collectors/load",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject multiline command, output=%s", out)
	}
	if !strings.Contains(string(out), "single line") {
		t.Fatalf("expected single-line error, got %s", out)
	}
}

func TestCollectorWrapperRejectsNestedCollectorPath(t *testing.T) {
	dir := t.TempDir()
	collectorPath := filepath.Join(dir, "disk")
	if err := os.WriteFile(collectorPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile collector returned error: %v", err)
	}

	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+dir,
		"SSH_ORIGINAL_COMMAND=HUGIN_CHECK_ID=x "+filepath.Join(dir, "nested", "disk"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject nested collector path, output=%s", out)
	}
	if !strings.Contains(string(out), "collector path must be") {
		t.Fatalf("expected collector path error, got %s", out)
	}
}

func TestCollectorWrapperRejectsUnknownCollector(t *testing.T) {
	dir := t.TempDir()
	collectorPath := filepath.Join(dir, "custom")
	if err := os.WriteFile(collectorPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0755); err != nil {
		t.Fatalf("WriteFile collector returned error: %v", err)
	}

	cmd := exec.Command("./hugin-collector-wrapper")
	cmd.Env = append(os.Environ(),
		"HUGIN_COLLECTOR_DIR="+dir,
		"SSH_ORIGINAL_COMMAND=HUGIN_CHECK_ID=x "+collectorPath,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected wrapper to reject unknown collector, output=%s", out)
	}
	if !strings.Contains(string(out), "not allowlisted") {
		t.Fatalf("expected allowlist error, got %s", out)
	}
}
