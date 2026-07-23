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
