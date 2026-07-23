package tools

import (
	"fmt"
	"strings"

	"github.com/levmv/golems/pkg/golem"
)

type CapabilityProfile struct {
	Name        string
	Description string
}

var capabilityProfileCatalog = []CapabilityProfile{
	{Name: "read-only", Description: "read, search, and available web · no writes"},
	{Name: "edit", Description: "read, write, and available web · no Bash"},
	{Name: "full", Description: "files, Bash, and available web tools"},
}

func CapabilityProfiles() []CapabilityProfile {
	return append([]CapabilityProfile(nil), capabilityProfileCatalog...)
}

func NormalizeCapabilityProfile(profile string) (string, error) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "full"
	}
	switch profile {
	case "full", "edit", "read-only":
		return profile, nil
	default:
		return "", fmt.Errorf("unknown capability profile %q (want full, edit, or read-only)", profile)
	}
}

func FilterForProfile(tools []golem.Tool, profile string) []golem.Tool {
	filtered := make([]golem.Tool, 0, len(tools))
	for _, tool := range tools {
		allowed := false
		switch profile {
		case "full":
			allowed = true
		case "edit":
			allowed = tool.Effect == golem.ToolEffectRead || tool.Effect == golem.ToolEffectWrite || tool.Effect == golem.ToolEffectExternal
		case "read-only":
			allowed = tool.Effect == golem.ToolEffectRead || tool.Effect == golem.ToolEffectExternal
		}
		if allowed {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
