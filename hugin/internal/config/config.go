package config

import (
	"time"
)

// Config represents the root of the hugin.yaml configuration file.
type Config struct {
	App       AppConfig           `yaml:"app"`
	LLM       LLMConfig           `yaml:"llm"`
	Targets   map[string]Target   `yaml:"targets"`
	Checks    []Check             `yaml:"checks"`
	Notifiers map[string]Notifier `yaml:"notifiers"`
}

type AppConfig struct {
	DataDir             string `yaml:"data_dir"`
	Timezone            string `yaml:"timezone"`
	MaxConcurrentChecks int    `yaml:"max_concurrent_checks"`
}

type LLMConfig struct {
	Provider     string  `yaml:"provider"`
	Model        string  `yaml:"model"`
	APIKeyEnv    string  `yaml:"api_key_env"`
	Temperature  float32 `yaml:"temperature"`
	MaxInputRuns int     `yaml:"max_input_runs"`
}

type Target struct {
	Type                  string `yaml:"type,omitempty"` // "local" or "ssh"
	Host                  string `yaml:"host,omitempty"`
	User                  string `yaml:"user,omitempty"`
	Key                   string `yaml:"key,omitempty"` // Path to SSH key
	KnownHosts            string `yaml:"known_hosts,omitempty"`
	InsecureIgnoreHostKey bool   `yaml:"insecure_ignore_host_key,omitempty"`
	Context               string `yaml:"context,omitempty"`
}

type Check struct {
	ID       string        `yaml:"id"`
	Target   string        `yaml:"target"`
	Command  string        `yaml:"command"`
	Schedule string        `yaml:"schedule"` // Cron expression
	Timeout  time.Duration `yaml:"timeout"`
	Context  string        `yaml:"context,omitempty"`
	Analysis Analysis      `yaml:"analysis"`
	Alert    Alert         `yaml:"alert"`
}

type Analysis struct {
	History string `yaml:"history"` // e.g., "7d"
}

type Alert struct {
	Cooldown         time.Duration `yaml:"cooldown"`
	RepeatAfter      time.Duration `yaml:"repeat_after"`
	NotifyOnResolved bool          `yaml:"notify_on_resolved"`
}

type Notifier struct {
	Enabled     bool   `yaml:"enabled"`
	BotTokenEnv string `yaml:"bot_token_env"`
	ChatIDEnv   string `yaml:"chat_id_env"`
}

func (c *Config) FindCheck(id string) *Check {
	for i := range c.Checks {
		if c.Checks[i].ID == id {
			return &c.Checks[i]
		}
	}
	return nil
}
