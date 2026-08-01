package main

import (
	"fmt"
	"slices"
	"strings"
)

const defaultReasoningEffort = ""

func reasoningEffortsForModel(uri string) []string {
	switch strings.ToLower(strings.TrimSpace(uri)) {
	case "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro":
		return []string{defaultReasoningEffort, "high"}
	default:
		return []string{defaultReasoningEffort}
	}
}

func normalizeReasoningEffort(uri, effort string) (string, error) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "default" {
		effort = defaultReasoningEffort
	}
	if slices.Contains(reasoningEffortsForModel(uri), effort) {
		return effort, nil
	}
	return "", fmt.Errorf("reasoning effort %q is unsupported for model %q", effort, strings.TrimSpace(uri))
}
