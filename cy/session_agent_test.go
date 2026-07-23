package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/cy/internal/state"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/hackernews"
	"github.com/levmv/golems/pkg/webfetch"
)

func TestSessionAgentLoginAddsAndRemovesWebSearch(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	home := t.TempDir()
	root := t.TempDir()
	journal, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(
		Config{Home: home, ModelURI: "fake/model", CapabilityProfile: "full", SandboxPolicy: sandboxOff},
		&runTurnFakeModel{}, root, nil, journal, store,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	before, err := agent.ContextReport()
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Login("tavily", "tvly-test-key"); err != nil {
		t.Fatal(err)
	}
	afterLogin, err := agent.ContextReport()
	if err != nil {
		t.Fatal(err)
	}
	if afterLogin.ToolTokens <= before.ToolTokens {
		t.Fatalf("tool tokens did not grow after Tavily login: before=%d after=%d", before.ToolTokens, afterLogin.ToolTokens)
	}
	if err := agent.Logout("tavily"); err != nil {
		t.Fatal(err)
	}
	afterLogout, err := agent.ContextReport()
	if err != nil {
		t.Fatal(err)
	}
	if afterLogout.ToolTokens != before.ToolTokens {
		t.Fatalf("tool tokens after logout = %d, want %d", afterLogout.ToolTokens, before.ToolTokens)
	}
}

func TestConfiguredToolsIncludeFetchAndCredentialedSearch(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent := &sessionAgent{state: store}
	tools, err := agent.toolsForProfile(nil, "read-only")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(tools); strings.Join(got, ",") != "web_fetch,hacker_news" {
		t.Fatalf("tools without search credential = %v", got)
	}
	if err := store.SetAPIKey("tavily", "tvly-test-key"); err != nil {
		t.Fatal(err)
	}
	tools, err = agent.toolsForProfile(nil, "read-only")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(tools); strings.Join(got, ",") != "web_fetch,hacker_news,web_search" {
		t.Fatalf("tools with search credential = %v", got)
	}
}

func TestWebFetchBackendsFollowConfiguredCredentialOrder(t *testing.T) {
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent := &sessionAgent{state: store}
	backends, err := agent.webFetchBackends(hackernews.NewClient())
	if err != nil {
		t.Fatal(err)
	}
	if got := backendNames(backends); strings.Join(got, ",") != "hacker_news,http" {
		t.Fatalf("backends without service credentials = %v", got)
	}
	if err := store.SetAPIKey("exa", "exa-test-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("firecrawl", "fc-test-key"); err != nil {
		t.Fatal(err)
	}
	backends, err = agent.webFetchBackends(hackernews.NewClient())
	if err != nil {
		t.Fatal(err)
	}
	if got := backendNames(backends); strings.Join(got, ",") != "hacker_news,http,firecrawl,exa" {
		t.Fatalf("credentialed backends = %v", got)
	}
}

func backendNames(backends []webfetch.Backend) []string {
	names := make([]string, 0, len(backends))
	for _, backend := range backends {
		names = append(names, backend.Name())
	}
	return names
}

func toolNames(tools []golem.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Definition.Function.Name)
	}
	return names
}

func TestSessionAgentSwitchModelPersistsJournalAndDefault(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	journal, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: "deepseek/deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(Config{ModelURI: "deepseek/deepseek-v4-flash", CapabilityProfile: "full", SandboxPolicy: sandboxOff}, &runTurnFakeModel{}, root, nil, journal, store)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if err := agent.SwitchModel("ollama/local-model"); err != nil {
		t.Fatal(err)
	}
	state, err := journal.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if state.Model != "ollama/local-model" {
		t.Fatalf("journal model = %q", state.Model)
	}
	stored, err := store.Config()
	if err != nil || stored.Model != "ollama/local-model" {
		t.Fatalf("settings = %#v, %v", stored, err)
	}
}

func TestSessionAgentSwitchProfilePersistsDefault(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	journal, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(Config{Home: home, ModelURI: "fake/model", CapabilityProfile: "full", SandboxPolicy: sandboxOff}, &runTurnFakeModel{}, root, nil, journal, store)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	defer agent.Close()

	if err := agent.SwitchProfile("edit"); err != nil {
		t.Fatal(err)
	}
	if agent.CurrentProfile() != "edit" {
		t.Fatalf("profile = %q", agent.CurrentProfile())
	}
	stored, err := store.Config()
	if err != nil || stored.Profile != "edit" {
		t.Fatalf("stored settings = %#v, %v", stored, err)
	}
}

func TestSessionAgentSwitchDoesNotMutateRuntimeWhenDefaultCannotBeSaved(t *testing.T) {
	stateHome := t.TempDir()
	store, err := state.Open(stateHome)
	if err != nil {
		t.Fatal(err)
	}
	sessionHome := t.TempDir()
	root := t.TempDir()
	journal, err := session.Create(session.CreateOptions{Home: sessionHome, Workspace: root, Model: "fake/model"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(Config{Home: sessionHome, ModelURI: "fake/model", CapabilityProfile: "full", SandboxPolicy: sandboxOff}, &runTurnFakeModel{}, root, nil, journal, store)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	if err := os.RemoveAll(stateHome); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SwitchModel("ollama/local-model"); err == nil {
		t.Fatal("SwitchModel() succeeded with an unwritable state store")
	}
	if got := agent.CurrentModel(); got != "fake/model" {
		t.Fatalf("model changed after persistence failure: %q", got)
	}
	replayed, err := journal.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Model != "fake/model" {
		t.Fatalf("journal model changed after persistence failure: %q", replayed.Model)
	}

	if err := agent.SwitchProfile("edit"); err == nil {
		t.Fatal("SwitchProfile() succeeded with an unwritable state store")
	}
	if got := agent.CurrentProfile(); got != "full" {
		t.Fatalf("profile changed after persistence failure: %q", got)
	}
}

func TestSessionAgentMissingCredentialFailsAtModelCallBoundary(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	home := t.TempDir()
	root := t.TempDir()
	journal, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: "deepseek/deepseek-v4-flash"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(Config{ModelURI: "deepseek/deepseek-v4-flash", CapabilityProfile: "full", SandboxPolicy: sandboxOff}, &runTurnFakeModel{}, root, nil, journal, store)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	_, err = agent.Stream(context.Background(), "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "use /login deepseek") {
		t.Fatalf("Stream() error = %v, want login guidance", err)
	}
	state, replayErr := journal.Replay()
	if replayErr != nil {
		t.Fatal(replayErr)
	}
	if len(state.Messages) != 0 {
		t.Fatalf("missing-credential turn changed history: %#v", state.Messages)
	}
}

func TestSessionAgentClearAndResumeSwitchDurableRuntime(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	cfg := Config{Home: home, RootDir: root, ModelURI: "fake/model"}
	initial, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: cfg.ModelURI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.Append(session.RecordUserMessage, session.UserMessage{RunID: "seed", Content: "keep this session"}); err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(cfg, &runTurnFakeModel{}, root, nil, initial, nil)
	if err != nil {
		_ = initial.Close()
		t.Fatal(err)
	}
	defer agent.Close()
	initialID := agent.SessionID()

	freshID, err := agent.ClearSession()
	if err != nil {
		t.Fatal(err)
	}
	if freshID == initialID || agent.SessionID() != freshID {
		t.Fatalf("clear switched to %q from %q; current=%q", freshID, initialID, agent.SessionID())
	}
	probe, err := session.Open(home, initialID)
	if err != nil {
		t.Fatalf("previous session was not closed after clear: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	resumedID, err := agent.ResumeSession(initialID[:12])
	if err != nil {
		t.Fatal(err)
	}
	if resumedID != initialID || agent.SessionID() != initialID {
		t.Fatalf("resume id=%q current=%q want=%q", resumedID, agent.SessionID(), initialID)
	}
	summaries, err := agent.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != initialID {
		t.Fatalf("summaries = %#v", summaries)
	}
}

func TestSessionAgentResumeRestoresSessionModel(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	currentModel := "deepseek/deepseek-v4-flash"
	resumedModel := "openai/test-model"
	initial, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: currentModel})
	if err != nil {
		t.Fatal(err)
	}
	target, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: resumedModel})
	if err != nil {
		t.Fatal(err)
	}
	targetID := target.ID()
	if _, err := target.Append(session.RecordUserMessage, session.UserMessage{RunID: "seed", Content: "resume me"}); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Home: home, RootDir: root, ModelURI: currentModel, SandboxPolicy: sandboxOff}
	agent, err := newSessionAgent(cfg, &runTurnFakeModel{}, root, nil, initial, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := agent.ResumeSession(targetID); err != nil {
		t.Fatal(err)
	}
	if got := agent.CurrentModel(); got != resumedModel {
		t.Fatalf("CurrentModel() = %q, want %q", got, resumedModel)
	}
	records, err := agent.journal.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Type == session.RecordModelChanged {
			t.Fatalf("resume rewrote session model: %#v", record)
		}
	}
}

func TestSessionAgentResumeReportsAlreadyOpenSession(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	cfg := Config{Home: home, RootDir: root, ModelURI: "fake/model"}
	initial, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: cfg.ModelURI})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(cfg, &runTurnFakeModel{}, root, nil, initial, nil)
	if err != nil {
		_ = initial.Close()
		t.Fatal(err)
	}
	defer agent.Close()
	locked, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: cfg.ModelURI})
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()

	_, err = agent.ResumeSession(locked.ID())
	if !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("ResumeSession() error = %v, want ErrSessionLocked", err)
	}
}

func TestSessionAgentListsAndResumesOnlyCurrentWorkspace(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	otherRoot := t.TempDir()
	cfg := Config{Home: home, RootDir: root, ModelURI: "fake/model"}
	initial, err := session.Create(session.CreateOptions{Home: home, Workspace: root, Model: cfg.ModelURI})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := newSessionAgent(cfg, &runTurnFakeModel{}, root, nil, initial, nil)
	if err != nil {
		_ = initial.Close()
		t.Fatal(err)
	}
	defer agent.Close()
	other, err := session.Create(session.CreateOptions{Home: home, Workspace: otherRoot, Model: cfg.ModelURI})
	if err != nil {
		t.Fatal(err)
	}
	otherID := other.ID()
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	summaries, err := agent.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("workspace-filtered summaries = %#v", summaries)
	}
	if _, err := agent.ResumeSession(otherID); err == nil || !strings.Contains(err.Error(), "current workspace") {
		t.Fatalf("cross-workspace resume error = %v", err)
	}
}
