package tools

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

//go:embed builtin_skills/*/SKILL.md
var builtinSkillsFS embed.FS

type Skill struct {
	Name        string
	Description string
	Path        string
	Content     string
}

type SkillLibrary struct {
	skills map[string]Skill
}

type skillReadArgs struct {
	Name string `json:"name"`
}

func NewBuiltinSkillLibrary() (*SkillLibrary, error) {
	matches, err := fs.Glob(builtinSkillsFS, "builtin_skills/*/SKILL.md")
	if err != nil {
		return nil, fmt.Errorf("glob builtin skills: %w", err)
	}
	lib := &SkillLibrary{skills: map[string]Skill{}}
	for _, path := range matches {
		b, err := builtinSkillsFS.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read builtin skill %s: %w", path, err)
		}
		skill, err := parseSkill(path, string(b))
		if err != nil {
			return nil, err
		}
		if _, exists := lib.skills[skill.Name]; exists {
			return nil, fmt.Errorf("duplicate builtin skill %q", skill.Name)
		}
		lib.skills[skill.Name] = skill
	}
	return lib, nil
}

func SkillTools(lib *SkillLibrary) []golem.Tool {
	return []golem.Tool{
		skillListTool(lib),
		skillReadTool(lib),
	}
}

func parseSkill(path, content string) (Skill, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, fmt.Errorf("skill %s: missing frontmatter", path)
	}
	name := ""
	description := ""
	end := -1
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			end = i
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	if end == -1 {
		return Skill{}, fmt.Errorf("skill %s: unterminated frontmatter", path)
	}
	if name == "" {
		return Skill{}, fmt.Errorf("skill %s: missing name", path)
	}
	if description == "" {
		return Skill{}, fmt.Errorf("skill %s: missing description", path)
	}
	return Skill{Name: name, Description: description, Path: path, Content: content}, nil
}

func skillListTool(lib *SkillLibrary) golem.Tool {
	schema := jsonschema.Obj()
	return golem.FunctionToolWithEffect(golem.ToolEffectRead, "skill_list",
		"List builtin Caliban skills. Skills are detailed operating procedures; read one before using a matching capability.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if lib == nil {
				return golem.ToolResult{Content: "skills are not enabled"}, nil
			}
			return golem.ToolResult{Content: lib.FormatList()}, nil
		})
}

func skillReadTool(lib *SkillLibrary) golem.Tool {
	schema := jsonschema.Obj(
		jsonschema.Required("name", jsonschema.Str{
			Description: "Skill name from skill_list, for example runners.",
		}),
	)
	return golem.FunctionToolWithEffect(golem.ToolEffectRead, "skill_read",
		"Read the full instructions for one builtin Caliban skill.",
		schema,
		func(ctx context.Context, call llm.ToolCall) (golem.ToolResult, error) {
			if lib == nil {
				return golem.ToolResult{Content: "skills are not enabled"}, nil
			}
			var args skillReadArgs
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				return golem.ToolResult{}, fmt.Errorf("invalid skill_read arguments: %w", err)
			}
			skill, ok := lib.Skill(args.Name)
			if !ok {
				return golem.ToolResult{Content: fmt.Sprintf("skill %q not found", args.Name)}, nil
			}
			return golem.ToolResult{Content: skill.Content}, nil
		})
}

func (l *SkillLibrary) Skill(name string) (Skill, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	skill, ok := l.skills[name]
	return skill, ok
}

func (l *SkillLibrary) FormatList() string {
	names := make([]string, 0, len(l.skills))
	for name := range l.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("available skills:\n")
	for _, name := range names {
		skill := l.skills[name]
		fmt.Fprintf(&b, "- %s: %s\n", skill.Name, skill.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}
