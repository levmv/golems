package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/levmv/golems/pkg/tasks"
	"gopkg.in/yaml.v3"
)

var checkIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Load reads a YAML configuration file from the given path and parses it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// Validate ensures the structural integrity of the configuration.
func (c *Config) Validate() error {
	if c.App.DataDir == "" {
		return fmt.Errorf("app.data_dir is required")
	}
	if c.App.Timezone == "" {
		c.App.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(c.App.Timezone); err != nil {
		return fmt.Errorf("app.timezone %q is invalid: %w", c.App.Timezone, err)
	}
	if c.App.MaxConcurrentChecks <= 0 {
		c.App.MaxConcurrentChecks = 1
	}
	if c.LLM.Provider == "" {
		return fmt.Errorf("llm.provider is required")
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if c.LLM.MaxInputRuns <= 0 {
		c.LLM.MaxInputRuns = 50
	}
	for name, target := range c.Targets {
		if target.Type == "" {
			if target.Host == "" || target.Host == "localhost" || target.Host == "127.0.0.1" {
				target.Type = "local"
			} else {
				target.Type = "ssh"
			}
		}
		switch target.Type {
		case "local":
			if target.Host == "" {
				target.Host = "localhost"
			}
		case "ssh":
			if target.Host == "" {
				return fmt.Errorf("target '%s' is ssh but host is empty", name)
			}
			if target.User == "" {
				return fmt.Errorf("target '%s' is ssh but user is empty", name)
			}
			if target.Key == "" {
				return fmt.Errorf("target '%s' is ssh but key is empty", name)
			}
		default:
			return fmt.Errorf("target '%s' has invalid type %q", name, target.Type)
		}
		c.Targets[name] = target
	}
	if len(c.Checks) == 0 {
		return fmt.Errorf("no checks defined")
	}

	seenChecks := make(map[string]struct{}, len(c.Checks))
	for i, check := range c.Checks {
		if check.ID == "" {
			return fmt.Errorf("check at index %d is missing an ID", i)
		}
		if !checkIDPattern.MatchString(check.ID) {
			return fmt.Errorf("check %q has invalid ID: only letters, digits, underscore, dot, and dash are allowed", check.ID)
		}
		if _, exists := seenChecks[check.ID]; exists {
			return fmt.Errorf("check '%s' is defined more than once", check.ID)
		}
		seenChecks[check.ID] = struct{}{}
		if check.Target == "" {
			return fmt.Errorf("check '%s' is missing a target", check.ID)
		}
		if check.Command == "" {
			return fmt.Errorf("check '%s' is missing a command", check.ID)
		}
		if check.Schedule == "" {
			return fmt.Errorf("check '%s' is missing a schedule", check.ID)
		}
		if err := tasks.Cron(check.Schedule, c.App.Timezone).Validate(); err != nil {
			return fmt.Errorf("check '%s' has invalid schedule %q: %w", check.ID, check.Schedule, err)
		}
		if check.Timeout <= 0 {
			return fmt.Errorf("check '%s' timeout must be positive", check.ID)
		}

		// Ensure the target referenced by the check actually exists
		if _, exists := c.Targets[check.Target]; !exists {
			return fmt.Errorf("check '%s' references unknown target '%s'", check.ID, check.Target)
		}
	}

	for name, ntf := range c.Notifiers {
		if !ntf.Enabled {
			continue
		}
		if ntf.BotTokenEnv == "" {
			return fmt.Errorf("notifier '%s' is enabled but bot_token_env is empty", name)
		}
		if ntf.ChatIDEnv == "" {
			return fmt.Errorf("notifier '%s' is enabled but chat_id_env is empty", name)
		}
	}

	return nil
}
