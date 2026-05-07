package runner

import (
	"context"
	"strings"
	"testing"
	"time"

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

func checkWithCommand(command string) config.Check {
	return config.Check{
		ID:      "disk",
		Command: command,
		Timeout: time.Second,
	}
}

func localTarget() config.Target {
	return config.Target{Host: "localhost"}
}

func hasErrorCode(errors []models.ErrorDetail, code string) bool {
	for _, err := range errors {
		if err.Code == code {
			return true
		}
	}
	return false
}
