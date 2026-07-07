package tools

import (
	"strings"
	"testing"
)

func TestBuiltinSkillsLoadAndRead(t *testing.T) {
	lib, err := NewBuiltinSkillLibrary()
	if err != nil {
		t.Fatalf("load builtin skills: %v", err)
	}
	for _, name := range []string{"background-tasks", "runners"} {
		skill, ok := lib.Skill(name)
		if !ok {
			t.Fatalf("skill %q not loaded; list:\n%s", name, lib.FormatList())
		}
		if skill.Description == "" || !strings.Contains(skill.Content, "# ") {
			t.Fatalf("skill %q has incomplete content: %+v", name, skill)
		}
	}

	out := runToolForTest(t, skillReadTool(lib), skillReadArgs{Name: "runners"})
	if !strings.Contains(out, "runner_run") || !strings.Contains(out, "workspace_access") {
		t.Fatalf("unexpected runners skill content:\n%s", out)
	}
}
