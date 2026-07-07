package main

import (
	"testing"

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

func TestSetWebPasswordHintUsesDefaultConfigPath(t *testing.T) {
	if got := setWebPasswordHint(defaultConfigPath); got != "caliban set-web-password" {
		t.Fatalf("default command = %q", got)
	}
	if got := setWebPasswordHint("dev-config.json"); got != "caliban set-web-password -config dev-config.json" {
		t.Fatalf("custom command = %q", got)
	}
}
