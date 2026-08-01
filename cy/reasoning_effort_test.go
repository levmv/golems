package main

import (
	"slices"
	"testing"
)

func TestReasoningEffortsForModel(t *testing.T) {
	for _, uri := range []string{"deepseek/deepseek-v4-flash", "openai/gpt-5", "openrouter/free", "ollama/local"} {
		if got := reasoningEffortsForModel(uri); !slices.Equal(got, []string{"", "high"}) {
			t.Fatalf("reasoning efforts for %s = %v", uri, got)
		}
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	for _, input := range []string{"", "default", " DEFAULT "} {
		got, err := normalizeReasoningEffort("deepseek/deepseek-v4-flash", input)
		if err != nil || got != "" {
			t.Fatalf("normalize %q = %q, %v", input, got, err)
		}
	}
	if got, err := normalizeReasoningEffort("openrouter/free", " HIGH "); err != nil || got != "high" {
		t.Fatalf("normalize high = %q, %v", got, err)
	}
	if _, err := normalizeReasoningEffort("openrouter/free", "medium"); err == nil {
		t.Fatal("unsupported effort was accepted")
	}
}
