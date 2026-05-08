package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAppliesDefaultsAndFindsChecks(t *testing.T) {
	cfg := Config{
		App: AppConfig{
			DataDir: "/tmp/hugin-test",
		},
		LLM: LLMConfig{
			Provider: "openai",
			Model:    "gpt-test",
		},
		Targets: map[string]Target{
			"local": {Host: "localhost"},
		},
		Checks: []Check{
			{
				ID:       "disk",
				Target:   "local",
				Command:  "collect-disk",
				Schedule: "*/5 * * * *",
				Timeout:  5 * time.Second,
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if cfg.App.Timezone != "UTC" {
		t.Fatalf("expected default timezone UTC, got %q", cfg.App.Timezone)
	}
	if cfg.App.MaxConcurrentChecks != 1 {
		t.Fatalf("expected default max concurrency 1, got %d", cfg.App.MaxConcurrentChecks)
	}
	if cfg.LLM.MaxInputRuns != 50 {
		t.Fatalf("expected default max input runs 50, got %d", cfg.LLM.MaxInputRuns)
	}
	if cfg.Targets["local"].Type != "local" {
		t.Fatalf("expected local target type default, got %q", cfg.Targets["local"].Type)
	}
	if got := cfg.FindCheck("disk"); got == nil || got.ID != "disk" {
		t.Fatalf("FindCheck did not return the configured check: %#v", got)
	}
	if got := cfg.FindCheck("missing"); got != nil {
		t.Fatalf("FindCheck returned unexpected check: %#v", got)
	}
}

func TestValidateRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "duplicate checks",
			mutate: func(cfg *Config) {
				cfg.Checks = append(cfg.Checks, cfg.Checks[0])
			},
			wantErr: "defined more than once",
		},
		{
			name: "unknown target",
			mutate: func(cfg *Config) {
				cfg.Checks[0].Target = "missing"
			},
			wantErr: "references unknown target",
		},
		{
			name: "missing llm provider",
			mutate: func(cfg *Config) {
				cfg.LLM.Provider = ""
			},
			wantErr: "llm.provider is required",
		},
		{
			name: "ssh target missing key",
			mutate: func(cfg *Config) {
				cfg.Targets["local"] = Target{Type: "ssh", Host: "example.test", User: "hugin"}
			},
			wantErr: "key is empty",
		},
		{
			name: "invalid schedule",
			mutate: func(cfg *Config) {
				cfg.Checks[0].Schedule = "not a cron"
			},
			wantErr: "invalid schedule",
		},
		{
			name: "missing notifier env",
			mutate: func(cfg *Config) {
				cfg.Notifiers = map[string]Notifier{
					"telegram": {Enabled: true, BotTokenEnv: "TOKEN"},
				}
			},
			wantErr: "chat_id_env is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		App: AppConfig{
			DataDir:  "/tmp/hugin-test",
			Timezone: "UTC",
		},
		LLM: LLMConfig{
			Provider: "openai",
			Model:    "gpt-test",
		},
		Targets: map[string]Target{
			"local": {Host: "localhost"},
		},
		Checks: []Check{
			{
				ID:       "disk",
				Target:   "local",
				Command:  "collect-disk",
				Schedule: "*/5 * * * *",
				Timeout:  5 * time.Second,
			},
		},
	}
}
