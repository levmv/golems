package tools

import (
	"context"
	"strings"
	"testing"
)

type fakeDelegator struct {
	prompt         string
	continueID     int64
	continuePrompt string
}

func (f *fakeDelegator) Delegate(_ context.Context, prompt string) (int64, string, error) {
	f.prompt = prompt
	return 42, "done", nil
}

func (f *fakeDelegator) DelegateContinue(_ context.Context, conversationID int64, prompt string) (string, error) {
	f.continueID = conversationID
	f.continuePrompt = prompt
	return "continued", nil
}

func TestDelegateToolRoundTrip(t *testing.T) {
	f := &fakeDelegator{}
	out, err := callTool(t, findTool(Delegation(f), "delegate"), map[string]any{
		"prompt": "check this",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if f.prompt != "check this" {
		t.Fatalf("prompt = %q", f.prompt)
	}
	if !strings.Contains(out, "child conversation 42") || !strings.Contains(out, "done") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestDelegateContinueToolRoundTrip(t *testing.T) {
	f := &fakeDelegator{}
	out, err := callTool(t, findTool(Delegation(f), "delegate_continue"), map[string]any{
		"conversation_id": 42,
		"prompt":          "follow up",
	})
	if err != nil {
		t.Fatalf("delegate_continue: %v", err)
	}
	if f.continueID != 42 || f.continuePrompt != "follow up" {
		t.Fatalf("continue call = id %d prompt %q", f.continueID, f.continuePrompt)
	}
	if !strings.Contains(out, "child conversation 42") || !strings.Contains(out, "continued") {
		t.Fatalf("unexpected output: %q", out)
	}
}
