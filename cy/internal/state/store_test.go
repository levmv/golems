package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSeparatesConfigAndAuth(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultModelSelection("openai/test", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultModelSelection("deepseek/deepseek-v4-flash", "high"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultProfile("edit"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("exa", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetModelContext(" OpenRouter/MoonshotAI/Kimi-K3 ", 1_048_576); err != nil {
		t.Fatal(err)
	}

	config, err := store.Config()
	if err != nil || config.Model != "deepseek/deepseek-v4-flash" || config.ReasoningEffort != "high" || config.Profile != "edit" || len(config.RecentModels) != 1 || config.RecentModels[0] != "openai/test" {
		t.Fatalf("config = %#v, %v", config, err)
	}
	key, ok, err := store.APIKey("exa")
	if err != nil || !ok || key != "secret" {
		t.Fatalf("credential = %q, %v, %v", key, ok, err)
	}
	window, ok, err := store.ModelContext("openrouter/moonshotai/kimi-k3")
	if err != nil || !ok || window != 1_048_576 {
		t.Fatalf("model context = %d, %v, %v", window, ok, err)
	}
	configRaw, err := os.ReadFile(filepath.Join(store.Dir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	authRaw, err := os.ReadFile(filepath.Join(store.Dir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configRaw), "secret") || strings.Contains(string(authRaw), config.Model) {
		t.Fatalf("config and auth were not separated: config=%s auth=%s", configRaw, authRaw)
	}
}
