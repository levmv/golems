package main

import (
	"strings"
	"testing"

	"github.com/levmv/golems/cy/internal/state"
)

func TestStoredCredentialsDoNotCompeteWithEnvironmentOverrides(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	key, err := storeProviderCredential(store, " OpenAI ", " stored-key ")
	if err != nil {
		t.Fatal(err)
	}
	if key != "stored-key" {
		t.Fatalf("stored key = %q", key)
	}

	t.Setenv("OPENAI_API_KEY", "environment-key")
	if _, err := storeProviderCredential(store, "openai", "replacement"); err == nil || !strings.Contains(err.Error(), "environment override") {
		t.Fatalf("login with environment override error = %v", err)
	}
	if err := deleteProviderCredential(store, "openai"); err == nil || !strings.Contains(err.Error(), "environment override") {
		t.Fatalf("logout with environment override error = %v", err)
	}
	stored, ok, err := store.APIKey("openai")
	if err != nil || !ok || stored != "stored-key" {
		t.Fatalf("stored credential = %q, %v, %v", stored, ok, err)
	}
}

func TestProviderStatusesSeparateModelsAndServices(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAVILY_API_KEY", "tavily-environment-key")
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	statuses, err := listProviderStatus(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 6 {
		t.Fatalf("statuses = %#v", statuses)
	}
	services := make(map[string]providerStatus)
	for _, status := range statuses {
		if status.Category == "Services" {
			services[status.Name] = status
		}
	}
	tavily := services["tavily"]
	if tavily.Name != "tavily" || tavily.Category != "Services" || tavily.Description != "web search" || tavily.Source != "environment override" || tavily.CredentialURL != "https://app.tavily.com" {
		t.Fatalf("tavily status = %#v", tavily)
	}
	if !isLoginProvider("tavily") || isModelLoginProvider("tavily") {
		t.Fatal("tavily credential classification is incorrect")
	}
	exa := services["exa"]
	if exa.Name != "exa" || exa.Description != "web search and fetch" || exa.Source != "none" || exa.CredentialURL != "https://dashboard.exa.ai/api-keys" || !isLoginProvider("exa") || isModelLoginProvider("exa") {
		t.Fatalf("exa status = %#v", exa)
	}
	firecrawl := services["firecrawl"]
	if firecrawl.Name != "firecrawl" || firecrawl.Description != "web fetch fallback" || firecrawl.Source != "none" || firecrawl.CredentialURL != "https://www.firecrawl.dev/app/api-keys" || !isLoginProvider("firecrawl") || isModelLoginProvider("firecrawl") {
		t.Fatalf("firecrawl status = %#v", firecrawl)
	}
}
