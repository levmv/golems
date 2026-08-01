package main

import (
	"cmp"
	"os"
	"strings"
)

const (
	defaultModelURI          = "deepseek/deepseek-v4-flash"
	defaultRootDir           = "."
	defaultCapabilityProfile = "full"
	defaultSandboxPolicy     = "auto"
)

type Config struct {
	ModelURI          string
	ReasoningEffort   string
	RootDir           string
	Home              string
	Verbose           bool
	JSON              bool
	CapabilityProfile string
	SandboxPolicy     string
	Security          SecurityState
	PrintMode         bool
	SaveSession       bool
	Ephemeral         bool
}

func LoadConfig() Config {
	return Config{
		ModelURI:          cmp.Or(os.Getenv("CY_MODEL"), defaultModelURI),
		RootDir:           cmp.Or(os.Getenv("CY_ROOT"), defaultRootDir),
		Home:              strings.TrimSpace(os.Getenv("CY_HOME")),
		CapabilityProfile: cmp.Or(strings.TrimSpace(os.Getenv("CY_PROFILE")), defaultCapabilityProfile),
		SandboxPolicy:     cmp.Or(strings.TrimSpace(os.Getenv("CY_SANDBOX")), defaultSandboxPolicy),
	}
}
