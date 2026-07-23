package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/hackernews"
	"github.com/levmv/golems/pkg/logger"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]logger.Level{
		"":      logger.LevelInfo,
		"info":  logger.LevelInfo,
		"DEBUG": logger.LevelDebug,
		"warn":  logger.LevelWarn,
		"error": logger.LevelError,
	}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Fatalf("parseLogLevel(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseLogLevel("loud"); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestValidateTelegramOptionalButConsistent(t *testing.T) {
	base := Config{
		DBPath:        "caliban.db",
		WorkspacePath: "workspace",
		Providers:     map[string]ProviderConfig{"openrouter": {APIKey: "sk-test"}},
		Models:        ModelsConfig{Main: "openrouter/test"},
	}
	if err := base.validate(); err != nil {
		t.Fatalf("telegram should be optional: %v", err)
	}

	onlyToken := base
	onlyToken.Telegram.Token = "token"
	if err := onlyToken.validate(); err == nil {
		t.Fatal("expected error when telegram token is set without chat_id")
	}

	fullTelegram := base
	fullTelegram.Telegram.Token = "token"
	fullTelegram.Telegram.ChatID = 123
	if err := fullTelegram.validate(); err != nil {
		t.Fatalf("full telegram config should validate: %v", err)
	}
}

func TestValidateWebPushConfig(t *testing.T) {
	base := Config{
		DBPath:        "caliban.db",
		WorkspacePath: "workspace",
		Providers:     map[string]ProviderConfig{"openrouter": {APIKey: "sk-test"}},
		Models:        ModelsConfig{Main: "openrouter/test"},
	}
	subjectOnly := base
	subjectOnly.Web.Push.Subject = "admin@example.com"
	if err := subjectOnly.validate(); err != nil {
		t.Fatalf("subject alone should leave web push disabled: %v", err)
	}

	missingPrivate := base
	missingPrivate.Web.Push.VAPIDPublicKey = "pub"
	missingPrivate.Web.Push.Subject = "admin@example.com"
	if err := missingPrivate.validate(); err == nil {
		t.Fatal("expected web.push with missing private key to fail")
	}

	missingSubject := base
	missingSubject.Web.Push.VAPIDPublicKey = "pub"
	missingSubject.Web.Push.VAPIDPrivateKey = "priv"
	if err := missingSubject.validate(); err == nil {
		t.Fatal("expected web.push with missing subject to fail")
	}

	withMailto := base
	withMailto.Web.Push = WebPushConfig{
		VAPIDPublicKey:  "pub",
		VAPIDPrivateKey: "priv",
		Subject:         "mailto:admin@example.com",
	}
	if err := withMailto.validate(); err == nil {
		t.Fatal("expected mailto: subject to fail")
	}

	full := base
	full.Web.Push = WebPushConfig{
		VAPIDPublicKey:  "pub",
		VAPIDPrivateKey: "priv",
		Subject:         "admin@example.com",
	}
	if err := full.validate(); err != nil {
		t.Fatalf("full web.push config should validate: %v", err)
	}
}

func TestWebAuthEnabledDefaultsToTrue(t *testing.T) {
	base := Config{
		DBPath:        "caliban.db",
		WorkspacePath: "workspace",
		Providers:     map[string]ProviderConfig{"openrouter": {APIKey: "sk-test"}},
		Models:        ModelsConfig{Main: "openrouter/test"},
	}
	if !base.Web.Auth.enabled() {
		t.Fatal("web auth should default to enabled")
	}
	disabled := false
	base.Web.Auth.Enabled = &disabled
	if base.Web.Auth.enabled() {
		t.Fatal("web auth should be explicitly disableable")
	}
	enabled := true
	base.Web.Auth.Enabled = &enabled
	if !base.Web.Auth.enabled() {
		t.Fatal("web auth should allow explicit true")
	}
}

func TestWebSearchCredentialsFollowFallbackOrder(t *testing.T) {
	base := Config{
		DBPath:        "caliban.db",
		WorkspacePath: "workspace",
		Providers:     map[string]ProviderConfig{"openrouter": {APIKey: "sk-test"}},
		Models:        ModelsConfig{Main: "openrouter/test"},
		Services: map[string]ServiceConfig{
			"tavily":    {APIKey: " tvly-test "},
			"exa":       {APIKey: "exa-test"},
			"firecrawl": {APIKey: " fc-test "},
		},
	}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	credentials := base.webSearchCredentials()
	if len(credentials) != 2 || credentials[0].Provider != "tavily" || credentials[0].Token != "tvly-test" || credentials[1].Provider != "exa" {
		t.Fatalf("default credentials = %#v", credentials)
	}
}

func TestWebFetchBackendsUseServiceFallbackOrder(t *testing.T) {
	config := Config{Services: map[string]ServiceConfig{
		"exa":       {APIKey: "exa-test"},
		"firecrawl": {APIKey: "fc-test"},
	}}
	backends := config.webFetchBackends(hackernews.NewClient())
	want := []string{"hacker_news", "firecrawl", "exa", "http"}
	if len(backends) != len(want) {
		t.Fatalf("backends = %#v", backends)
	}
	for i, backend := range backends {
		if backend.Name() != want[i] {
			t.Fatalf("backend %d = %q, want %q", i, backend.Name(), want[i])
		}
	}

	config.Services["firecrawl"] = ServiceConfig{}
	backends = config.webFetchBackends(hackernews.NewClient())
	if len(backends) != 3 || backends[0].Name() != "hacker_news" || backends[1].Name() != "exa" || backends[2].Name() != "http" {
		t.Fatalf("backends without Firecrawl = %#v", backends)
	}

	backends = (&Config{}).webFetchBackends(hackernews.NewClient())
	if len(backends) != 2 || backends[0].Name() != "hacker_news" || backends[1].Name() != "http" {
		t.Fatalf("backends without services = %#v", backends)
	}
}

func TestLoadConfigRejectsUnknownAndTrailingFields(t *testing.T) {
	valid := `{
		"db_path": "caliban.db",
		"workspace_path": "workspace",
		"providers": {"openrouter": {"api_key": "sk-test"}},
		"models": {"main": "openrouter/test"}
	}`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown top-level field",
			body: strings.Replace(valid, `"models":`, `"service": {}, "models":`, 1),
			want: `unknown field "service"`,
		},
		{
			name: "unknown nested field",
			body: strings.Replace(valid, `"api_key": "sk-test"`, `"api_key": "sk-test", "api_token": "typo"`, 1),
			want: `unknown field "api_token"`,
		},
		{
			name: "trailing value",
			body: valid + `{}`,
			want: "multiple JSON values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadConfig error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsUnsupportedServiceAndNegativeLimits(t *testing.T) {
	base := Config{
		DBPath:        "caliban.db",
		WorkspacePath: "workspace",
		Providers:     map[string]ProviderConfig{"openrouter": {APIKey: "sk-test"}},
		Models:        ModelsConfig{Main: "openrouter/test"},
	}

	unsupported := base
	unsupported.Services = map[string]ServiceConfig{"tavili": {APIKey: "typo"}}
	if err := unsupported.validate(); err == nil || !strings.Contains(err.Error(), "unsupported service") {
		t.Fatalf("unsupported service error = %v", err)
	}

	negative := base
	negative.Shell.TimeoutSeconds = -1
	if err := negative.validate(); err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
		t.Fatalf("negative timeout error = %v", err)
	}
}

func TestSetWebPasswordHintUsesDefaultConfigPath(t *testing.T) {
	if got := setWebPasswordHint(defaultConfigPath); got != "caliban set-web-password" {
		t.Fatalf("default command = %q", got)
	}
	if got := setWebPasswordHint("dev-config.json"); got != "caliban set-web-password -config dev-config.json" {
		t.Fatalf("custom command = %q", got)
	}
}
