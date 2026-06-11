package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/hugin/internal/models"
)

func TestExecuteLocalValidCollectorOutput(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' '{"check":"disk","status":"ok","metrics":{"used_pct":72.5}}'`), localTarget())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output.Check != "disk" {
		t.Fatalf("expected check disk, got %q", output.Check)
	}
	if output.Status != models.StatusOK {
		t.Fatalf("expected status ok, got %q", output.Status)
	}
	if output.Metrics["used_pct"] != 72.5 {
		t.Fatalf("expected used_pct metric, got %#v", output.Metrics["used_pct"])
	}
}

func TestExecuteLocalInjectsCheckID(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '{"check":"%s","status":"ok"}\n' "$HUGIN_CHECK_ID"`), localTarget())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output.Check != "disk" {
		t.Fatalf("output check = %q, want disk", output.Check)
	}
}

func TestExecuteLocalIgnoresStdoutNoiseAroundCollectorJSON(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' 'tput: unknown terminal' '[Warning] {not json}' '{"check":"disk","status":"ok","metrics":{"used_pct":72.5}}' '[Warning] disk is slow'`), localTarget())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output.Status != models.StatusOK {
		t.Fatalf("expected status ok, got %q", output.Status)
	}
	if output.Metrics["used_pct"] != 72.5 {
		t.Fatalf("expected used_pct metric, got %#v", output.Metrics["used_pct"])
	}
}

func TestExecuteLocalAcceptsScalarMetricTypes(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' '{"check":"disk","status":"ok","metrics":{"used_pct":72.5,"state":"steady","backup_enabled":true}}'`), localTarget())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output.Metrics["state"] != "steady" {
		t.Fatalf("expected string metric to be preserved, got %#v", output.Metrics["state"])
	}
	if output.Metrics["backup_enabled"] != true {
		t.Fatalf("expected bool metric to be preserved, got %#v", output.Metrics["backup_enabled"])
	}
}

func TestExecuteLocalNonZeroCollectorOutput(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' '{"check":"disk","status":"error","errors":[{"code":"DISK_CHECK_FAILED","message":"df failed"}]}'; exit 7`), localTarget())
	if err == nil {
		t.Fatalf("Execute returned nil error")
	}
	if output.Status != models.StatusError {
		t.Fatalf("expected status error, got %q", output.Status)
	}
	if !hasErrorCode(output.Errors, "DISK_CHECK_FAILED") {
		t.Fatalf("expected collector error to be preserved, got %#v", output.Errors)
	}
	if !hasErrorCode(output.Errors, "EXECUTION_NON_ZERO") {
		t.Fatalf("expected execution error to be appended, got %#v", output.Errors)
	}
}

func TestExecuteLocalRejectsNestedMetricValues(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "object",
			json: `{"check":"disk","status":"ok","metrics":{"nested":{"value":1}}}`,
		},
		{
			name: "array",
			json: `{"check":"disk","status":"ok","metrics":{"samples":[1,2,3]}}`,
		},
		{
			name: "null",
			json: `{"check":"disk","status":"ok","metrics":{"missing":null}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' '`+tt.json+`'`), localTarget())
			if err == nil {
				t.Fatalf("Execute returned nil error")
			}
			if output.Status != models.StatusError {
				t.Fatalf("expected status error, got %q", output.Status)
			}
			if !hasErrorCode(output.Errors, "INVALID_OUTPUT") {
				t.Fatalf("expected invalid output error, got %#v", output.Errors)
			}
		})
	}
}

func TestExecuteLocalInvalidJSONReturnsStructuredError(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf 'not json'`), localTarget())
	if err == nil {
		t.Fatalf("Execute returned nil error")
	}
	if output.Status != models.StatusError {
		t.Fatalf("expected status error, got %q", output.Status)
	}
	if !hasErrorCode(output.Errors, "EXECUTION_FAILED") {
		t.Fatalf("expected execution failure error, got %#v", output.Errors)
	}
}

func TestExecuteLocalInvalidJSONTruncatesDiagnostics(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%05000d' 1; printf '%05000d' 2 >&2`), localTarget())
	if err == nil {
		t.Fatalf("Execute returned nil error")
	}
	if len(output.Errors) != 1 {
		t.Fatalf("errors = %#v", output.Errors)
	}
	message := output.Errors[0].Message
	if len(message) > diagnosticOutputLimit*2+512 {
		t.Fatalf("diagnostic message too long: %d bytes", len(message))
	}
	if !strings.Contains(message, "...(truncated)") {
		t.Fatalf("diagnostic message did not mention truncation: %q", message)
	}
}

func TestExecuteLocalRejectsWrongCheckID(t *testing.T) {
	output, err := Execute(context.Background(), checkWithCommand(`printf '%s\n' '{"check":"other","status":"ok"}'`), localTarget())
	if err == nil {
		t.Fatalf("Execute returned nil error")
	}
	if output.Status != models.StatusError {
		t.Fatalf("expected status error, got %q", output.Status)
	}
	if !hasErrorCode(output.Errors, "INVALID_OUTPUT") {
		t.Fatalf("expected invalid output error, got %#v", output.Errors)
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected check mismatch error, got %v", err)
	}
}

func TestHostKeyCallbackRequiresKnownHostsByDefault(t *testing.T) {
	_, err := hostKeyCallback(config.Target{
		Host:       "example.test",
		KnownHosts: filepath.Join(t.TempDir(), "missing_known_hosts"),
	})
	if err == nil {
		t.Fatal("expected missing known_hosts file to fail")
	}
	if !strings.Contains(err.Error(), "known_hosts") {
		t.Fatalf("expected known_hosts error, got %v", err)
	}
}

func TestHostKeyCallbackCanBeExplicitlyInsecure(t *testing.T) {
	callback, err := hostKeyCallback(config.Target{InsecureIgnoreHostKey: true})
	if err != nil {
		t.Fatalf("hostKeyCallback returned error: %v", err)
	}
	if callback == nil {
		t.Fatal("expected insecure host key callback")
	}
}

func TestIsLocalTargetTrustsExplicitType(t *testing.T) {
	if !isLocalTarget(config.Target{Type: "local", Host: "localhost"}) {
		t.Fatal("local target was not treated as local")
	}
	if isLocalTarget(config.Target{Type: "ssh", Host: "localhost"}) {
		t.Fatal("ssh localhost target was treated as local")
	}
}

func TestWithCheckIDEnvPrefixesSSHCommand(t *testing.T) {
	got := withCheckIDEnv("disk-web1", "/opt/hugin/collectors/disk")
	want := "HUGIN_CHECK_ID=disk-web1 /opt/hugin/collectors/disk"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestTruncateDiagnosticPreservesUTF8(t *testing.T) {
	got := truncateDiagnostic(strings.Repeat("я", diagnosticOutputLimit))
	if !strings.Contains(got, "...(truncated)") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateDiagnostic returned invalid UTF-8")
	}
}

func checkWithCommand(command string) config.Check {
	return config.Check{
		ID:      "disk",
		Command: command,
		Timeout: time.Second,
	}
}

func localTarget() config.Target {
	return config.Target{Type: "local", Host: "localhost"}
}

func hasErrorCode(errors []models.ErrorDetail, code string) bool {
	for _, err := range errors {
		if err.Code == code {
			return true
		}
	}
	return false
}
