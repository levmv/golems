package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/levmv/golems/cy/internal/session"
	"github.com/levmv/golems/pkg/llm"
)

func TestManualCompactionIsAdditiveAndRebuildsContext(t *testing.T) {
	s := contextSession(t)
	appendCompletedTurn(t, s, "run-1", "old question", "old answer")
	appendCompletedTurn(t, s, "run-2", "recent question", "recent answer")
	model := &scriptedModel{chatResponses: []*llm.Response{{
		Content: "Objective: continue the recent work. Old decision: keep the journal additive.",
		Usage:   llm.Usage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42},
	}}}
	eng, err := New(Config{Model: model, Session: s, ModelURI: "test/model", ContextWindow: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	report, _, err := eng.Compact(context.Background(), "retain decisions")
	if err != nil {
		t.Fatal(err)
	}
	if report.CompactionCount != 1 || report.SummaryTokens == 0 {
		t.Fatalf("context report = %#v", report)
	}
	state, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 4 {
		t.Fatalf("compaction deleted original messages: %#v", state.Messages)
	}
	if state.Compaction == nil || state.Compaction.FirstVerbatimSeq == 0 || !strings.Contains(state.Compaction.Summary, "journal additive") {
		t.Fatalf("compaction state = %#v", state.Compaction)
	}
	messages, _ := eng.buildContext(state)
	joined := messageContents(messages)
	if strings.Contains(joined, "old question") || !strings.Contains(joined, "Conversation summary") || !strings.Contains(joined, "recent question") {
		t.Fatalf("rebuilt context = %q", joined)
	}
}

func TestAutoCompactionRunsAtSafeRequestBoundary(t *testing.T) {
	s := contextSession(t)
	appendCompletedTurn(t, s, "run-1", strings.Repeat("old context ", 3000), "old answer")
	appendCompletedTurn(t, s, "run-2", "recent question", "recent answer")
	model := &scriptedModel{chatResponses: []*llm.Response{
		{Content: "compact summary", Usage: llm.Usage{TotalTokens: 10}},
		{Content: "final answer", FinishReason: llm.FinishReasonStop},
	}}
	eng, err := New(Config{Model: model, Session: s, ModelURI: "unknown/model", ContextWindow: 12 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := eng.Stream(context.Background(), "new question", nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Reply != "final answer" || len(model.requests) != 2 {
		t.Fatalf("turn=%#v requests=%d", turn, len(model.requests))
	}
	if len(model.requests[0].Tools) != 0 || !strings.Contains(model.requests[0].Messages[0].Content, "Summarize an older segment") {
		t.Fatalf("first request is not the summarizer: %#v", model.requests[0])
	}
	if joined := messageContents(model.requests[1].Messages); strings.Contains(joined, "old context") || !strings.Contains(joined, "compact summary") || !strings.Contains(joined, "new question") {
		t.Fatalf("post-compaction request = %q", joined)
	}
	state, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if state.CompactionCount != 1 {
		t.Fatalf("compaction count = %d", state.CompactionCount)
	}
}

func TestContextReportIncludesPendingInputAndConservativeLimit(t *testing.T) {
	s := contextSession(t)
	model := &scriptedModel{}
	eng, err := New(Config{Model: model, Session: s, ModelURI: "unknown/model", ContextWindow: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.QueueInput("remember this follow-up"); err != nil {
		t.Fatal(err)
	}
	report, err := eng.ContextReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.InputLimit != 24*1024 || report.PendingTokens == 0 || report.PercentLeft <= 0 || report.PercentLeft > 100 {
		t.Fatalf("report = %#v", report)
	}
}

func TestCompactionKeepsOverflowingNewerTurnsVerbatim(t *testing.T) {
	oldQuestion := "old-question " + strings.Repeat("q", 20*1024)
	oldAnswer := "old-answer " + strings.Repeat("a", 20*1024)
	recentQuestion := "recent-question " + strings.Repeat("q", 20*1024)
	recentAnswer := "recent-answer " + strings.Repeat("a", 20*1024)
	state := session.State{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: oldQuestion},
			{Role: llm.RoleAI, Content: oldAnswer},
			{Role: llm.RoleUser, Content: recentQuestion},
			{Role: llm.RoleAI, Content: recentAnswer},
		},
		MessageSeqs: []uint64{1, 2, 3, 4},
	}
	eng := &Engine{contextWindow: 1}
	prompt, compactedEnd := eng.compactionPrompt(state, 0, len(state.Messages), "")
	if compactedEnd != 2 {
		t.Fatalf("compacted end = %d, want complete first turn boundary 2", compactedEnd)
	}
	if !strings.Contains(prompt, "old-question") || strings.Contains(prompt, "recent-question") {
		t.Fatalf("bounded compaction prompt selected wrong turns")
	}
	if !strings.Contains(prompt, "newer records left verbatim") {
		t.Fatalf("bounded compaction prompt omitted boundary note: %q", prompt)
	}
}

func TestCompactionPromptIncludesToolCallNameAndArguments(t *testing.T) {
	state := session.State{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "run the tests"},
			{Role: llm.RoleAI, ToolCalls: []llm.ToolCall{{
				Function: llm.ToolFunction{Name: "bash", Arguments: `{"command":"go test ./cy/...","workdir":"."}`},
			}}},
			{Role: llm.RoleTool, Content: "status: completed\nexit_code: 0\n\nok"},
		},
		MessageSeqs: []uint64{1, 2, 3},
	}
	eng := &Engine{contextWindow: 32 * 1024}
	prompt, compactedEnd := eng.compactionPrompt(state, 0, len(state.Messages), "")
	if compactedEnd != len(state.Messages) {
		t.Fatalf("compacted end = %d", compactedEnd)
	}
	if !strings.Contains(prompt, `tool_call: bash args={"command":"go test ./cy/...","workdir":"."}`) {
		t.Fatalf("compaction prompt omitted tool call: %q", prompt)
	}
}

func TestCompactionRejectsTruncatedSummary(t *testing.T) {
	s := contextSession(t)
	appendCompletedTurn(t, s, "run-1", "old question", "old answer")
	appendCompletedTurn(t, s, "run-2", "recent question", "recent answer")
	model := &scriptedModel{chatResponses: []*llm.Response{{
		Content:      "partial summary",
		FinishReason: llm.FinishReasonLength,
	}}}
	eng, err := New(Config{Model: model, Session: s, ContextWindow: 32 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.Compact(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "incomplete summary") {
		t.Fatalf("Compact() error = %v", err)
	}
	state, err := s.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if state.Compaction != nil {
		t.Fatalf("truncated summary advanced compaction boundary: %#v", state.Compaction)
	}
}

func contextSession(t *testing.T) *session.Session {
	t.Helper()
	s, err := session.Create(session.CreateOptions{Home: t.TempDir(), Workspace: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appendCompletedTurn(t *testing.T, s *session.Session, runID, input, output string) {
	t.Helper()
	if _, err := s.Append(session.RecordUserMessage, session.UserMessage{RunID: runID, Content: input}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(session.RecordAssistantMessage, session.AssistantMessage{RunID: runID, Content: output}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(session.RecordRunFinished, session.RunFinished{RunID: runID}); err != nil {
		t.Fatal(err)
	}
}

func messageContents(messages []llm.Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}
