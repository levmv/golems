package tools

import (
	"context"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

func TestCapabilityProfilesRemoveWholeToolClasses(t *testing.T) {
	run := func(context.Context, llm.ToolCall) (golem.ToolResult, error) { return golem.ToolResult{}, nil }
	tools := []golem.Tool{
		golem.FunctionToolWithEffect(golem.ToolEffectRead, "read", "", jsonschema.Object(nil), run),
		golem.FunctionToolWithEffect(golem.ToolEffectWrite, "write", "", jsonschema.Object(nil), run),
		golem.FunctionToolWithEffect(golem.ToolEffectProcess, "bash", "", jsonschema.Object(nil), run),
		golem.FunctionToolWithEffect(golem.ToolEffectExternal, "web_fetch", "", jsonschema.Object(nil), run),
	}
	checks := map[string][]string{
		"full":      {"read", "write", "bash", "web_fetch"},
		"edit":      {"read", "write", "web_fetch"},
		"read-only": {"read", "web_fetch"},
	}
	for profile, want := range checks {
		filtered := FilterForProfile(tools, profile)
		if len(filtered) != len(want) {
			t.Fatalf("profile %s tools=%d want=%d", profile, len(filtered), len(want))
		}
		for index := range want {
			if got := filtered[index].Definition.Function.Name; got != want[index] {
				t.Errorf("profile %s tool %d=%q want=%q", profile, index, got, want[index])
			}
		}
	}
}

func TestLSIsOnlyExposedWithoutBash(t *testing.T) {
	workspace, _, err := NewWorkspaceTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		profile string
		want    bool
	}{
		{profile: "full", want: false},
		{profile: "edit", want: true},
		{profile: "read-only", want: true},
	} {
		found := false
		workspaceForProfile := FilterWorkspaceToolsForProfile(workspace, test.profile)
		for _, tool := range FilterForProfile(workspaceForProfile, test.profile) {
			if tool.Definition.Function.Name == lsToolName {
				found = true
			}
		}
		if found != test.want {
			t.Errorf("profile %s exposes ls=%v, want %v", test.profile, found, test.want)
		}
	}
}

func TestGenericProfileFilterDoesNotSpecialCaseToolNames(t *testing.T) {
	run := func(context.Context, llm.ToolCall) (golem.ToolResult, error) { return golem.ToolResult{}, nil }
	tool := golem.FunctionToolWithEffect(golem.ToolEffectRead, lsToolName, "external ls", jsonschema.Object(nil), run)
	for _, profile := range []string{"full", "edit", "read-only"} {
		filtered := FilterForProfile([]golem.Tool{tool}, profile)
		if len(filtered) != 1 {
			t.Errorf("profile %s removed an unrelated read tool named ls", profile)
		}
	}
}
