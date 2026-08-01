package main

import (
	"slices"
	"testing"
)

func TestReasoningEffortsForModel(t *testing.T) {
	for _, uri := range []string{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro"} {
		if got := reasoningEffortsForModel(uri); !slices.Equal(got, []string{"", "high"}) {
			t.Fatalf("reasoning efforts for %s = %v", uri, got)
		}
	}
	if got := reasoningEffortsForModel("openrouter/free"); !slices.Equal(got, []string{""}) {
		t.Fatalf("unknown model efforts = %v", got)
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	for _, input := range []string{"", "default", " DEFAULT "} {
		got, err := normalizeReasoningEffort("deepseek/deepseek-v4-flash", input)
		if err != nil || got != "" {
			t.Fatalf("normalize %q = %q, %v", input, got, err)
		}
	}
	if got, err := normalizeReasoningEffort("deepseek/deepseek-v4-flash", " HIGH "); err != nil || got != "high" {
		t.Fatalf("normalize high = %q, %v", got, err)
	}
	if _, err := normalizeReasoningEffort("openrouter/free", "high"); err == nil {
		t.Fatal("unsupported effort was accepted")
	}
}
