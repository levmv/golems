package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestDispatchRouting pins the parts of command routing that have a contract
// beyond "call the handler": the help/usage cases must succeed, and an
// unrecognized command must return errUnknownCommand (which main maps to exit 2).
func TestDispatchRouting(t *testing.T) {
	defer silenceStdio(t)()

	for _, args := range [][]string{nil, {}, {"help"}, {"-h"}, {"-help"}, {"--help"}} {
		if err := dispatch(args); err != nil {
			t.Errorf("dispatch(%q) = %v, want nil", args, err)
		}
	}

	if err := dispatch([]string{"bogus"}); !errors.Is(err, errUnknownCommand) {
		t.Errorf("dispatch([bogus]) = %v, want errUnknownCommand", err)
	}
}

func TestCheckConfigValidatesStaticRuntimeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"db_path": "caliban.db",
		"workspace_path": "workspace",
		"providers": {"openrouter": {"api_key": "sk-test"}},
		"models": {"main": "openrouter/test"},
		"timezone": "UTC",
		"log_level": "info"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(path); err != nil {
		t.Fatalf("checkConfig(valid) = %v", err)
	}

	badTimezone := []byte(`{
		"db_path": "caliban.db",
		"workspace_path": "workspace",
		"providers": {"openrouter": {"api_key": "sk-test"}},
		"models": {"main": "openrouter/test"},
		"timezone": "Not/A-Timezone"
	}`)
	if err := os.WriteFile(path, badTimezone, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(path); err == nil {
		t.Fatal("checkConfig should reject an invalid timezone")
	}
}

// silenceStdio redirects os.Stdout/os.Stderr to /dev/null for the duration of a
// test so usage banners do not pollute test output, restoring them on cleanup.
func silenceStdio(t *testing.T) func() {
	t.Helper()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	return func() {
		os.Stdout, os.Stderr = origOut, origErr
		devnull.Close()
	}
}
