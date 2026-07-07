package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/caliban/internal/workspace"
	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/jsonschema"
	"github.com/levmv/golems/pkg/llm"
)

func fakeTool(name string) golem.Tool {
	return golem.FunctionTool(name, name, jsonschema.Obj(),
		func(context.Context, llm.ToolCall) (golem.ToolResult, error) { return golem.ToolResult{}, nil })
}

func toolNames(tools []golem.Tool) map[string]bool {
	m := make(map[string]bool, len(tools))
	for _, t := range tools {
		m[t.Definition.Function.Name] = true
	}
	return m
}

func freeTimeTestEngine(t *testing.T, tools []golem.Tool) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.EnsureMainConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(t.TempDir())
	eng, err := New(Config{Store: st, Workspace: ws, Main: &scriptModel{}, MainModelID: "fake/main", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestFreeTimeToolsStripsUnsafeTools(t *testing.T) {
	// New also appends history_search + delegate/delegate_continue.
	eng := freeTimeTestEngine(t, []golem.Tool{
		fakeTool("shell"), fakeTool("memory_upsert"),
		fakeTool("notify"), fakeTool("schedule_turn"), fakeTool("runner_run"),
	})

	got := toolNames(eng.freeTimeTools())
	for _, keep := range []string{"shell", "memory_upsert", "history_search"} {
		if !got[keep] {
			t.Errorf("free-time tools missing %q", keep)
		}
	}
	for _, strip := range []string{"notify", "schedule_turn", "runner_run", "delegate", "delegate_continue"} {
		if got[strip] {
			t.Errorf("free-time tools should not include %q", strip)
		}
	}
}

func TestRunProfileFreeTimeVsDefault(t *testing.T) {
	eng := freeTimeTestEngine(t, []golem.Tool{fakeTool("shell"), fakeTool("notify")})
	// Pretend free-time resolved its conversation to this id (Start does this via
	// the sentinel-uuid lookup). The default branch must apply to every other id.
	eng.freeTimeConvID = 42

	def := eng.runProfile(1)
	if len(def.tools) != len(eng.tools) {
		t.Fatalf("default profile should use all tools: got %d want %d", len(def.tools), len(eng.tools))
	}
	if def.maxToolIterations != eng.maxToolIter {
		t.Fatalf("default maxToolIterations = %d, want %d", def.maxToolIterations, eng.maxToolIter)
	}
	if def.systemPrompt("base") != "base" {
		t.Fatalf("default profile must not alter the prompt: %q", def.systemPrompt("base"))
	}

	ft := eng.runProfile(eng.freeTimeConvID)
	if toolNames(ft.tools)["notify"] {
		t.Fatal("free-time profile must strip notify")
	}
	if ft.maxToolIterations != eng.maxToolIter {
		t.Fatalf("free-time maxToolIterations = %d, want %d", ft.maxToolIterations, eng.maxToolIter)
	}
	if !strings.Contains(ft.systemPrompt("base"), freeTimePrompt) || !strings.HasPrefix(ft.systemPrompt("base"), "base") {
		t.Fatalf("free-time profile must append the free-time prompt to the base")
	}
}

// TestRunProfileDisabledNeverMatches pins the latent-bug fix: with free-time off
// (freeTimeConvID == 0), no conversation gets the free-time profile — not even one
// that reused the old reserved id 3 (e.g. a delegation child conversation).
func TestRunProfileDisabledNeverMatches(t *testing.T) {
	eng := freeTimeTestEngine(t, []golem.Tool{fakeTool("shell"), fakeTool("notify")})
	if eng.freeTimeConvID != 0 {
		t.Fatalf("freeTimeConvID should be 0 when free-time is disabled, got %d", eng.freeTimeConvID)
	}
	for _, id := range []int64{1, 2, 3} {
		if p := eng.runProfile(id); len(p.tools) != len(eng.tools) || p.maxToolIterations != eng.maxToolIter {
			t.Fatalf("conversation %d got a non-default profile while free-time is disabled", id)
		}
	}
}

func TestFreeTimeDisabledByDefault(t *testing.T) {
	// The build ships with free-time off; enabling is a deliberate code change.
	if freeTimeEnabled {
		t.Fatal("freeTimeEnabled must be false in committed code")
	}
}

// TestFreeTimeConversationUUIDResolves guards the sentinel constant: it must be a
// valid UUIDv7 (the store rejects anything else), resolve to a top-level
// conversation, and ensure idempotently to the same id.
func TestFreeTimeConversationUUIDResolves(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	c1, err := st.EnsureConversationByUUID(ctx, freeTimeConversationUUID)
	if err != nil {
		t.Fatalf("ensure free-time conversation: %v", err)
	}
	if c1.ParentRunID != nil {
		t.Fatalf("free-time conversation must be top-level, got parent %v", *c1.ParentRunID)
	}
	c2, err := st.EnsureConversationByUUID(ctx, freeTimeConversationUUID)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if c1.ID != c2.ID {
		t.Fatalf("not idempotent: first id %d, second id %d", c1.ID, c2.ID)
	}
}
