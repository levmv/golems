package main

import (
	"testing"

	"github.com/levmv/golems/hugin/internal/deploy"
	"github.com/levmv/golems/hugin/internal/storage"
)

func TestParseRunsArgs(t *testing.T) {
	checkID, limit, err := parseRunsArgs([]string{"disk", "--last", "3"})
	if err != nil {
		t.Fatalf("parseRunsArgs returned error: %v", err)
	}
	if checkID != "disk" || limit != 3 {
		t.Fatalf("expected disk/3, got %q/%d", checkID, limit)
	}

	checkID, limit, err = parseRunsArgs([]string{"--last=2", "memory"})
	if err != nil {
		t.Fatalf("parseRunsArgs with equals returned error: %v", err)
	}
	if checkID != "memory" || limit != 2 {
		t.Fatalf("expected memory/2, got %q/%d", checkID, limit)
	}
}

func TestParseResolveNote(t *testing.T) {
	note, err := parseResolveNote([]string{"--note", "fixed", "disk"})
	if err != nil {
		t.Fatalf("parseResolveNote returned error: %v", err)
	}
	if note != "fixed disk" {
		t.Fatalf("expected joined note, got %q", note)
	}

	note, err = parseResolveNote([]string{"manual", "resolution"})
	if err != nil {
		t.Fatalf("parseResolveNote without flag returned error: %v", err)
	}
	if note != "manual resolution" {
		t.Fatalf("expected legacy note, got %q", note)
	}
}

func TestParsePositiveInt64(t *testing.T) {
	got, err := parsePositiveInt64("42")
	if err != nil {
		t.Fatalf("parsePositiveInt64 returned error: %v", err)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if _, err := parsePositiveInt64("0"); err == nil {
		t.Fatal("parsePositiveInt64(0) error = nil, want error")
	}
}

func TestParseDoctorArgs(t *testing.T) {
	opts, err := parseDoctorArgs([]string{"--no-ssh", "--ssh-timeout=2s"})
	if err != nil {
		t.Fatalf("parseDoctorArgs returned error: %v", err)
	}
	if opts.CheckSSH {
		t.Fatal("expected --no-ssh to disable SSH checks")
	}
	if opts.SSHTimeout.String() != "2s" {
		t.Fatalf("expected timeout 2s, got %s", opts.SSHTimeout)
	}
}

func TestParseDeployArgs(t *testing.T) {
	target, opts, err := parseDeployArgs([]string{"web1", "--source", "hugin/collectors", "--dest=/tmp/hugin"})
	if err != nil {
		t.Fatalf("parseDeployArgs returned error: %v", err)
	}
	if target != "web1" {
		t.Fatalf("expected web1 target, got %q", target)
	}
	if opts.Source != "hugin/collectors" || opts.Dest != "/tmp/hugin" {
		t.Fatalf("unexpected opts: %+v", opts)
	}

	_, opts, err = parseDeployArgs([]string{"web1"})
	if err != nil {
		t.Fatalf("parseDeployArgs defaults returned error: %v", err)
	}
	if opts.Dest != deploy.DefaultCollectorsDest {
		t.Fatalf("expected default dest %q, got %q", deploy.DefaultCollectorsDest, opts.Dest)
	}
}

func TestFormatRunAnalysisStatusPrioritizesPipelineFailure(t *testing.T) {
	severity, summary := formatRunAnalysisStatus(&storage.RunAnalysis{
		Severity:      "urgent",
		Summary:       "Analysis completed",
		PipelineError: "incident update failed",
	})
	if severity != "failed" {
		t.Fatalf("severity = %q, want failed", severity)
	}
	if summary != "analysis pipeline failed: incident update failed" {
		t.Fatalf("summary = %q", summary)
	}
}
