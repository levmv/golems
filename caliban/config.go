package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/hackernews"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/logger"
	"github.com/levmv/golems/pkg/openai"
	"github.com/levmv/golems/pkg/webfetch"
	"github.com/levmv/golems/pkg/websearch"
)

// Config is the on-disk configuration. Secrets live here (outside the workspace
// and the scrubbed shell env), following ann's example precedent.
type Config struct {
	DBPath        string                    `json:"db_path"`
	WorkspacePath string                    `json:"workspace_path"`
	Providers     map[string]ProviderConfig `json:"providers"`
	Services      map[string]ServiceConfig  `json:"services"`
	Models        ModelsConfig              `json:"models"`
	Telegram      TelegramConfig            `json:"telegram"`
	Shell         ShellConfig               `json:"shell"`
	Context       ContextConfig             `json:"context"`
	Web           WebConfig                 `json:"web"`
	Timezone      string                    `json:"timezone"`
	LogLevel      string                    `json:"log_level"` // debug | info | warn | error; default info
	// MaxToolIterations caps tool-call rounds per user-facing run; 0 uses the
	// engine default (40). Free-time runs use their own tighter ceiling.
	MaxToolIterations int `json:"max_tool_iterations"`
}

type WebConfig struct {
	Addr           string        `json:"addr"`            // listen address, e.g. "127.0.0.1:8721"; empty disables the web transport
	ConversationID int64         `json:"conversation_id"` // optional; default 2 (web main)
	Auth           WebAuthConfig `json:"auth"`
	Push           WebPushConfig `json:"push"`
}

type WebAuthConfig struct {
	Enabled *bool `json:"enabled"` // optional; defaults to true when web.addr is set
}

type WebPushConfig struct {
	VAPIDPublicKey  string `json:"vapid_public_key"`
	VAPIDPrivateKey string `json:"vapid_private_key"`
	// Subject is the VAPID contact identity: either an email address or an
	// https:// URL. Do not include a mailto: prefix; webpush-go adds it.
	Subject string `json:"subject"`
}

type ProviderConfig struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

type ServiceConfig struct {
	APIKey string `json:"api_key"`
}

type ModelsConfig struct {
	Main  string `json:"main"`
	Cheap string `json:"cheap"`
}

type TelegramConfig struct {
	Token          string `json:"token"`
	ChatID         int64  `json:"chat_id"`
	ConversationID int64  `json:"conversation_id"` // optional; default 1 (telegram main)
}

type ShellConfig struct {
	TimeoutSeconds int `json:"timeout_seconds"`
	MaxOutputBytes int `json:"max_output_bytes"`
	// Sandbox selects the per-command shell isolation policy (Linux/Landlock):
	// "require" (fail closed if unavailable), "auto" (sandbox when available,
	// else run unsandboxed), or "off". Empty defaults to "auto". Use "require"
	// on the server.
	Sandbox string `json:"sandbox"`
}

type ContextConfig struct {
	// TailBudgetTokens triggers compaction when the post-summary tail exceeds it;
	// 0 uses the engine default (48000).
	TailBudgetTokens int `json:"tail_budget_tokens"`
	// KeepRecentTokens is the most-recent tail window kept verbatim on compaction
	// (the older remainder is folded into the summary); 0 uses the engine default
	// (24000). Clamped below TailBudgetTokens.
	KeepRecentTokens int `json:"keep_recent_tokens"`
}

// loadConfig reads, parses, and validates the config file.
func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse config: multiple JSON values")
		}
		return nil, fmt.Errorf("parse config: trailing data: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	switch {
	case c.DBPath == "":
		return fmt.Errorf("config: db_path is required")
	case c.WorkspacePath == "":
		return fmt.Errorf("config: workspace_path is required")
	case c.Models.Main == "":
		return fmt.Errorf("config: models.main is required")
	case len(c.Providers) == 0:
		return fmt.Errorf("config: at least one provider is required")
	}
	if (c.Telegram.Token == "") != (c.Telegram.ChatID == 0) {
		return fmt.Errorf("config: telegram.token and telegram.chat_id must be set together")
	}
	if c.Telegram.ConversationID < 0 {
		return fmt.Errorf("config: telegram.conversation_id must be positive")
	}
	if c.Web.ConversationID < 0 {
		return fmt.Errorf("config: web.conversation_id must be positive")
	}
	if c.MaxToolIterations < 0 {
		return fmt.Errorf("config: max_tool_iterations must not be negative")
	}
	if c.Shell.TimeoutSeconds < 0 {
		return fmt.Errorf("config: shell.timeout_seconds must not be negative")
	}
	if c.Shell.MaxOutputBytes < 0 {
		return fmt.Errorf("config: shell.max_output_bytes must not be negative")
	}
	if c.Context.TailBudgetTokens < 0 {
		return fmt.Errorf("config: context.tail_budget_tokens must not be negative")
	}
	if c.Context.KeepRecentTokens < 0 {
		return fmt.Errorf("config: context.keep_recent_tokens must not be negative")
	}
	for name := range c.Services {
		switch name {
		case "tavily", "exa", "firecrawl":
		default:
			return fmt.Errorf("config: unsupported service %q", name)
		}
	}
	pushKeysSet := c.Web.Push.VAPIDPublicKey != "" || c.Web.Push.VAPIDPrivateKey != ""
	pushComplete := c.Web.Push.VAPIDPublicKey != "" && c.Web.Push.VAPIDPrivateKey != "" && c.Web.Push.Subject != ""
	if pushKeysSet && !pushComplete {
		return fmt.Errorf("config: web.push requires vapid_public_key, vapid_private_key, and subject")
	}
	if pushKeysSet && strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.Web.Push.Subject)), "mailto:") {
		return fmt.Errorf("config: web.push.subject should be an email address or https:// URL (no mailto: prefix)")
	}
	switch c.Shell.Sandbox {
	case "", "require", "auto", "off":
	default:
		return fmt.Errorf("config: invalid shell.sandbox %q (use require|auto|off)", c.Shell.Sandbox)
	}
	return nil
}

func (c *Config) webSearchCredentials() []websearch.Credential {
	credentials := make([]websearch.Credential, 0, 2)
	for _, provider := range []string{"tavily", "exa"} {
		token := c.serviceKey(provider)
		if token == "" {
			continue
		}
		credentials = append(credentials, websearch.Credential{Provider: provider, Token: token})
	}
	return credentials
}

func (c *Config) webFetchBackends(hnClient *hackernews.Client) []webfetch.Backend {
	backends := []webfetch.Backend{hackernews.NewFetchBackend(hnClient)}
	for _, provider := range []string{"firecrawl", "exa"} {
		token := c.serviceKey(provider)
		if token == "" {
			continue
		}
		switch provider {
		case "firecrawl":
			backends = append(backends, webfetch.NewFirecrawlBackend(token))
		case "exa":
			backends = append(backends, webfetch.NewExaBackend(token))
		}
	}
	backends = append(backends, webfetch.NewHTTPBackend())
	return backends
}

func (c *Config) serviceKey(name string) string {
	return strings.TrimSpace(c.Services[name].APIKey)
}

func (c WebAuthConfig) enabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// logger builds the leveled logger from the config. An empty level means info.
func (c *Config) logger() (logger.Logger, error) {
	level, err := parseLogLevel(c.LogLevel)
	if err != nil {
		return nil, err
	}
	return logger.New(logger.Config{Level: level}), nil
}

func parseLogLevel(s string) (logger.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return logger.LevelInfo, nil
	case "debug":
		return logger.LevelDebug, nil
	case "warn", "warning":
		return logger.LevelWarn, nil
	case "error":
		return logger.LevelError, nil
	default:
		return 0, fmt.Errorf("config: invalid log_level %q (use debug|info|warn|error)", s)
	}
}

// timezone resolves the configured timezone, defaulting to UTC.
func (c *Config) timezone() (*time.Location, error) {
	if c.Timezone == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("config: load timezone %q: %w", c.Timezone, err)
	}
	return loc, nil
}

// registry builds an llm.Registry from the configured providers.
func (c *Config) registry() *llm.Registry {
	r := llm.NewRegistry()
	for name, p := range c.Providers {
		var opts []llm.ProviderOption
		if p.BaseURL != "" {
			baseURL := p.BaseURL
			opts = append(opts, func(cfg *openai.ClientConfig) { cfg.BaseURL = baseURL })
		}
		r.WithProvider(name, p.APIKey, opts...)
	}
	return r
}

// model resolves a configured model URI and attaches request logging.
func (c *Config) model(r *llm.Registry, uri string, log llm.Logger) (llm.Model, error) {
	m, err := r.Model(uri)
	if err != nil {
		return llm.Model{}, fmt.Errorf("config: model %q: %w", uri, err)
	}
	return m.WithLogging(log), nil
}
