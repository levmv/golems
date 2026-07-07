package main

import (
	"errors"
	"os"
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
