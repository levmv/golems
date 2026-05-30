package main

import (
	"testing"

	"github.com/levmv/golems/hugin/internal/deploy"
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
