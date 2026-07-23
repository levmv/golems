package tools

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if RunSandboxChildIfRequested() {
		return
	}
	os.Exit(m.Run())
}
