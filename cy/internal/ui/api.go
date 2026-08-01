package ui

import (
	"context"
	"strings"

	"github.com/levmv/golems/cy/internal/engine"
	"github.com/levmv/golems/cy/internal/session"
	toolruntime "github.com/levmv/golems/cy/internal/tools"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

var defaultProviders = []string{"deepseek", "openai", "openrouter"}

var capabilityProfileCatalog = toolruntime.CapabilityProfiles()

type Config struct {
	ModelURI          string
	ReasoningEffort   string
	CapabilityProfile string
	SecuritySummary   string
	Providers         []string
}

func (c Config) providers() []string {
	if len(c.Providers) > 0 {
		return c.Providers
	}
	return defaultProviders
}

type ProviderStatus struct {
	Name          string
	Source        string
	Category      string
	Description   string
	CredentialURL string
}

type providerStatus = ProviderStatus

type agentRunner interface {
	Stream(context.Context, string, golem.StreamFunc) (*golem.Turn, error)
}

type Agent interface {
	agentRunner
	SessionHistory() ([]llm.Message, error)
	SessionUsage() (llm.Usage, error)
	SessionID() string
	SessionRepaired() bool
	QueueInput(string) error
	ClaimQueued() (string, bool, error)
	RestoreQueued() ([]string, error)
	ClearSession() (string, error)
	ResumeSession(string) (string, error)
	ListSessions() ([]session.Summary, error)
	ContextReport() (engine.ContextReport, error)
	CachedContextReport() engine.ContextReport
	Compact(context.Context, string) (engine.ContextReport, error)
	ProviderStatuses() ([]ProviderStatus, error)
	Login(string, string) error
	Logout(string) error
	SwitchModelWithEffort(string, string) error
	KnownModels() []string
	CurrentModel() string
	CurrentReasoningEffort() string
	ReasoningEfforts(string) []string
	CurrentProfile() string
	SwitchProfile(string) error
	ProcessStatus(string) (toolruntime.ProcessResultMeta, bool)
}

type screenAgent = Agent

func modelProvider(uri string) string {
	provider, modelID, ok := strings.Cut(strings.TrimSpace(uri), "/")
	if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(modelID) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

func preview(text string, maxLen int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	if maxLen <= 0 {
		return ""
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
