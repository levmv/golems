package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/levmv/golems/caliban/internal/engine"
	"github.com/levmv/golems/caliban/internal/store"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/tasks"
	tasksqlite "github.com/levmv/golems/pkg/tasks/sqlite"
)

func inspectContext(args []string) error {
	fs := flag.NewFlagSet("inspect-context", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", defaultConfigPath, "path to config.json")
	conversationID := fs.Int64("conversation", 0, "conversation id; default is the configured web conversation, then telegram/main")
	messageLimit := fs.Int("messages", 30, "recent transcript messages to print")
	runLimit := fs.Int("runs", 8, "recent runs to print")
	summaryLimit := fs.Int("summaries", 5, "recent summaries to print")
	summaryChars := fs.Int("summary-chars", 700, "max characters from the latest summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	loc, err := cfg.timezone()
	if err != nil {
		return err
	}
	if *conversationID == 0 {
		*conversationID = defaultInspectConversationID(cfg)
	}
	if *messageLimit < 0 || *runLimit < 0 || *summaryLimit < 0 || *summaryChars < 0 {
		return fmt.Errorf("inspect-context: limits must be non-negative")
	}

	ctx := context.Background()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	conv, err := st.Conversation(ctx, *conversationID)
	if err != nil {
		return err
	}
	stats, err := loadInspectStats(ctx, st, conv.ID)
	if err != nil {
		return err
	}
	covered, err := st.CoveredThrough(ctx, conv.ID)
	if err != nil {
		return err
	}
	dueInput, hasDueInput, err := st.NextDueInput(ctx, conv.ID)
	if err != nil {
		return err
	}
	latestSummary, hasSummary, err := st.LatestSummary(ctx, conv.ID)
	if err != nil {
		return err
	}
	afterID := int64(0)
	if hasSummary {
		afterID = latestSummary.ThroughMessageID
	}
	tail, err := st.MessagesAfter(ctx, conv.ID, afterID)
	if err != nil {
		return err
	}
	summarizedMessages := 0
	if hasSummary {
		summarizedMessages, err = countInspectMessagesThrough(ctx, st, conv.ID, latestSummary.ThroughMessageID)
		if err != nil {
			return err
		}
	}

	budget := cfg.Context.TailBudgetTokens
	if budget <= 0 {
		budget = 48000
	}
	keep := cfg.Context.KeepRecentTokens
	if keep <= 0 {
		keep = 24000
	}
	if keep >= budget {
		keep = budget / 2
	}
	tailTokens := inspectMessagesTokens(tail)
	fmt.Printf("Caliban context inspection\n")
	fmt.Printf("config: %s\n", *configPath)
	fmt.Printf("db: %s\n", cfg.DBPath)
	fmt.Printf("conversation: %d uuid=%s status=%s created=%s\n", conv.ID, conv.UUID, conv.Status, inspectTime(conv.CreatedAt, loc))
	printInspectTranscriptStats(stats, loc)
	fmt.Printf("tail budget: %d tokens (rough estimate)\n", budget)
	fmt.Printf("cheap model: %s\n", inspectOptional(cfg.Models.Cheap, "not configured; compaction disabled"))
	printInspectCoverage(covered, dueInput, hasDueInput, loc)
	fmt.Println()

	if hasSummary {
		fmt.Printf("latest summary: #%d through message #%d at %s (%s ago), %d chars, est %d tokens\n",
			latestSummary.ID, latestSummary.ThroughMessageID, inspectTime(latestSummary.CreatedAt, loc),
			inspectAge(latestSummary.CreatedAt), len(latestSummary.Content), inspectTextTokens(latestSummary.Content))
		if strings.TrimSpace(latestSummary.Content) != "" && *summaryChars > 0 {
			fmt.Printf("summary excerpt:\n%s\n\n", inspectIndent(inspectTruncate(latestSummary.Content, *summaryChars), "  "))
		} else {
			fmt.Println()
		}
	} else {
		fmt.Println("latest summary: none")
		fmt.Println()
	}

	if hasSummary {
		fmt.Printf("summary coverage: %d messages folded, %d messages in post-summary tail\n", summarizedMessages, len(tail))
	} else {
		fmt.Printf("summary coverage: no summary yet, %d messages in initial tail\n", len(tail))
	}
	fmt.Printf("post-summary tail: %d messages, est %d tokens", len(tail), tailTokens)
	if budget > 0 {
		fmt.Printf(" (%.1f%% of budget)", 100*float64(tailTokens)/float64(budget))
	}
	fmt.Println()
	if len(tail) > 0 {
		fmt.Printf("tail span: #%d %s (%s ago) -> #%d %s (%s ago)\n",
			tail[0].ID, inspectTime(tail[0].CreatedAt, loc), inspectAge(tail[0].CreatedAt),
			tail[len(tail)-1].ID, inspectTime(tail[len(tail)-1].CreatedAt, loc), inspectAge(tail[len(tail)-1].CreatedAt))
	}
	printCompactionStatus(cfg.Models.Cheap != "", budget, keep, tail, tailTokens, covered)
	fmt.Println()

	printCompactionTask(ctx, st, conv.ID)
	fmt.Println()

	summaries, err := loadInspectSummaries(ctx, st, conv.ID, *summaryLimit)
	if err != nil {
		return err
	}
	printInspectSummaries(summaries, loc)
	fmt.Println()

	runs, err := loadInspectRuns(ctx, st, conv.ID, *runLimit)
	if err != nil {
		return err
	}
	printInspectRuns(runs, loc)
	fmt.Println()

	messages, err := loadInspectMessages(ctx, st, conv.ID, *messageLimit)
	if err != nil {
		return err
	}
	printInspectMessages(messages, hasSummary, latestSummary, loc)
	return nil
}

func defaultInspectConversationID(cfg *Config) int64 {
	if cfg.Web.Addr != "" {
		if cfg.Web.ConversationID != 0 {
			return cfg.Web.ConversationID
		}
		return defaultWebConvID
	}
	if cfg.Telegram.Token != "" {
		if cfg.Telegram.ConversationID != 0 {
			return cfg.Telegram.ConversationID
		}
		return defaultTelegramConvID
	}
	return defaultTelegramConvID
}

func printCompactionStatus(hasCheap bool, budget, keep int, tail []store.Message, tailTokens int, covered int64) {
	if !hasCheap {
		fmt.Println("compaction: disabled; models.cheap is not configured")
		return
	}
	if budget <= 0 || tailTokens <= budget {
		fmt.Println("compaction: not due; tail is within budget")
		return
	}
	fold := inspectFoldPoint(tail, keep)
	for fold > 0 && tail[fold-1].ID > covered {
		fold--
	}
	if fold <= 0 {
		fmt.Printf("compaction: due, but no safe covered messages to fold yet (cover=%d)\n", covered)
		return
	}
	folded := tail[:fold]
	kept := tail[fold:]
	fmt.Printf("compaction: due; next fold candidate %d/%d messages through #%d, folded_est=%d kept_est=%d\n",
		len(folded), len(tail), folded[len(folded)-1].ID, inspectMessagesTokens(folded), inspectMessagesTokens(kept))
	if len(kept) > 0 && kept[0].Role != llm.RoleUser {
		fmt.Printf("warning: post-fold tail would start with %s message #%d\n", kept[0].Role, kept[0].ID)
	}
}

func printCompactionTask(ctx context.Context, st *store.Store, conversationID int64) {
	taskStore, err := tasksqlite.New(st.DB(), taskStoreOptions)
	if err != nil {
		fmt.Printf("compaction task: unavailable: %v\n", err)
		return
	}
	t, err := taskStore.Get(ctx, fmt.Sprintf("%s-%d", engine.KindCompaction, conversationID))
	if errors.Is(err, tasks.ErrNotFound) {
		fmt.Println("compaction task: none")
		return
	}
	if err != nil {
		fmt.Printf("compaction task: unavailable: %v\n", err)
		return
	}
	state := "pending"
	if t.LockedAt != nil {
		state = "locked"
	}
	if t.Exhausted() {
		state = "exhausted"
	}
	fmt.Printf("compaction task: %s attempts=%d/%d next=%s last_error=%q\n",
		state, t.Attempts, t.MaxAttempts, inspectOptionalTime(t.NextRunAt), inspectTruncate(t.LastError, 160))
}

type inspectStats struct {
	Total     int
	ByRole    map[string]int
	FirstID   int64
	FirstTime time.Time
	LastID    int64
	LastTime  time.Time
}

func printInspectTranscriptStats(stats inspectStats, loc *time.Location) {
	fmt.Printf("transcript: %d messages", stats.Total)
	for _, role := range []string{string(llm.RoleUser), string(llm.RoleAI), string(llm.RoleTool), string(store.RoleEvent)} {
		if n := stats.ByRole[role]; n > 0 {
			fmt.Printf(" %s=%d", role, n)
		}
	}
	fmt.Println()
	if stats.Total > 0 {
		fmt.Printf("transcript span: #%d %s (%s ago) -> #%d %s (%s ago)\n",
			stats.FirstID, inspectTime(stats.FirstTime, loc), inspectAge(stats.FirstTime),
			stats.LastID, inspectTime(stats.LastTime, loc), inspectAge(stats.LastTime))
	}
}

func printInspectCoverage(covered int64, due store.Message, hasDue bool, loc *time.Location) {
	fmt.Printf("covered user message id: %d\n", covered)
	if !hasDue {
		fmt.Println("next due user input: none")
		return
	}
	fmt.Printf("next due user input: #%d at %s (%s ago) %s\n",
		due.ID, inspectTime(due.CreatedAt, loc), inspectAge(due.CreatedAt), inspectMessagePreview(due))
}

func printInspectSummaries(summaries []store.Summary, loc *time.Location) {
	fmt.Println("recent summaries:")
	if len(summaries) == 0 {
		fmt.Println("  none")
		return
	}
	for _, sm := range summaries {
		fmt.Printf("  #%d through msg #%d at %s chars=%d est=%d text=%q\n",
			sm.ID, sm.ThroughMessageID, inspectTime(sm.CreatedAt, loc), len(sm.Content),
			inspectTextTokens(sm.Content), inspectTruncate(oneLine(sm.Content), 120))
	}
}

func printInspectRuns(runs []store.Run, loc *time.Location) {
	fmt.Println("recent runs:")
	if len(runs) == 0 {
		fmt.Println("  none")
		return
	}
	for _, r := range runs {
		input := "-"
		if r.InputMessageID != nil {
			input = fmt.Sprintf("#%d", *r.InputMessageID)
		}
		fmt.Printf("  #%d %s initiator=%s input=%s model=%s tokens=%d/%d created=%s finished=%s",
			r.ID, r.Status, r.Initiator, input, r.Model, r.InputTokens, r.OutputTokens,
			inspectTime(r.CreatedAt, loc), inspectOptionalTimeIn(r.FinishedAt, loc))
		if r.Error != "" {
			fmt.Printf(" error=%q", inspectTruncate(r.Error, 140))
		}
		fmt.Println()
	}
}

func printInspectMessages(messages []store.Message, hasSummary bool, summary store.Summary, loc *time.Location) {
	fmt.Println("recent transcript:")
	if len(messages) == 0 {
		fmt.Println("  none")
		return
	}
	if hasSummary && messages[0].ID > summary.ThroughMessageID {
		fmt.Printf("  --- latest summary #%d folds through message #%d before this window ---\n",
			summary.ID, summary.ThroughMessageID)
	}
	for _, m := range messages {
		run := "-"
		if m.RunID != nil {
			run = fmt.Sprintf("#%d", *m.RunID)
		}
		source := m.Source
		if source == "" {
			source = "-"
		}
		fmt.Printf("  #%d %s role=%s source=%s run=%s est=%d %s\n",
			m.ID, inspectTime(m.CreatedAt, loc), m.Role, source, run,
			inspectContentTokens(m.Content), inspectMessagePreview(m))
		if hasSummary && m.ID == summary.ThroughMessageID {
			fmt.Printf("  --- latest summary #%d folds through here ---\n", summary.ID)
		}
	}
}

func loadInspectStats(ctx context.Context, st *store.Store, conversationID int64) (inspectStats, error) {
	stats := inspectStats{ByRole: make(map[string]int)}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT role, COUNT(*)
		 FROM messages
		 WHERE conversation_id = ?
		 GROUP BY role`, conversationID)
	if err != nil {
		return stats, fmt.Errorf("load transcript stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return stats, fmt.Errorf("scan transcript stats: %w", err)
		}
		stats.ByRole[role] = n
		stats.Total += n
	}
	if err := rows.Err(); err != nil {
		return stats, err
	}
	if stats.Total == 0 {
		return stats, nil
	}

	var firstAt, lastAt int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT id, created_at FROM messages WHERE conversation_id = ? ORDER BY id ASC LIMIT 1`,
		conversationID).Scan(&stats.FirstID, &firstAt); err != nil {
		return stats, fmt.Errorf("load first message: %w", err)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT id, created_at FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`,
		conversationID).Scan(&stats.LastID, &lastAt); err != nil {
		return stats, fmt.Errorf("load last message: %w", err)
	}
	stats.FirstTime = time.UnixMilli(firstAt).UTC()
	stats.LastTime = time.UnixMilli(lastAt).UTC()
	return stats, nil
}

func countInspectMessagesThrough(ctx context.Context, st *store.Store, conversationID, throughMessageID int64) (int, error) {
	var n int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM messages
		 WHERE conversation_id = ? AND id <= ?`,
		conversationID, throughMessageID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count summarized messages: %w", err)
	}
	return n, nil
}

func loadInspectSummaries(ctx context.Context, st *store.Store, conversationID int64, limit int) ([]store.Summary, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT id, conversation_id, through_message_id, content, created_at
		 FROM summaries
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("load summaries: %w", err)
	}
	defer rows.Close()

	var out []store.Summary
	for rows.Next() {
		var sm store.Summary
		var createdAt int64
		if err := rows.Scan(&sm.ID, &sm.ConversationID, &sm.ThroughMessageID, &sm.Content, &createdAt); err != nil {
			return nil, fmt.Errorf("scan summary: %w", err)
		}
		sm.CreatedAt = time.UnixMilli(createdAt).UTC()
		out = append(out, sm)
	}
	return out, rows.Err()
}

func loadInspectRuns(ctx context.Context, st *store.Store, conversationID int64, limit int) ([]store.Run, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT id, conversation_id, input_message_id, initiator, model, status,
		        input_tokens, output_tokens, error, created_at, finished_at
		 FROM runs
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("load runs: %w", err)
	}
	defer rows.Close()

	var out []store.Run
	for rows.Next() {
		var (
			r          store.Run
			inputID    sql.NullInt64
			errMsg     sql.NullString
			createdAt  int64
			finishedAt sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.ConversationID, &inputID, &r.Initiator, &r.Model, &r.Status,
			&r.InputTokens, &r.OutputTokens, &errMsg, &createdAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		if inputID.Valid {
			r.InputMessageID = &inputID.Int64
		}
		if errMsg.Valid {
			r.Error = errMsg.String
		}
		r.CreatedAt = time.UnixMilli(createdAt).UTC()
		if finishedAt.Valid {
			t := time.UnixMilli(finishedAt.Int64).UTC()
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadInspectMessages(ctx context.Context, st *store.Store, conversationID int64, limit int) ([]store.Message, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := st.DB().QueryContext(ctx,
		`SELECT id, conversation_id, run_id, role, source, content, created_at
		 FROM messages
		 WHERE conversation_id = ?
		 ORDER BY id DESC LIMIT ?`, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()

	var out []store.Message
	for rows.Next() {
		var (
			m          store.Message
			runID      sql.NullInt64
			role       string
			contentRaw string
			createdAt  int64
		)
		if err := rows.Scan(&m.ID, &m.ConversationID, &runID, &role, &m.Source, &contentRaw, &createdAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if runID.Valid {
			m.RunID = &runID.Int64
		}
		m.Role = llm.Role(role)
		if err := json.Unmarshal([]byte(contentRaw), &m.Content); err != nil {
			return nil, fmt.Errorf("decode message %d: %w", m.ID, err)
		}
		m.CreatedAt = time.UnixMilli(createdAt).UTC() // messages.created_at is unix millis
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func inspectFoldPoint(msgs []store.Message, keepTokens int) int {
	total := 0
	k := len(msgs)
	for k > 0 {
		t := inspectContentTokens(msgs[k-1].Content)
		if total+t > keepTokens {
			break
		}
		total += t
		k--
	}
	for k < len(msgs) && msgs[k].Role == llm.RoleTool {
		k++
	}
	if k >= len(msgs) {
		return len(msgs) - 1
	}
	return k
}

func inspectMessagesTokens(msgs []store.Message) int {
	total := 0
	for _, m := range msgs {
		total += inspectContentTokens(m.Content)
	}
	return total
}

func inspectContentTokens(c store.Content) int {
	n := len(c.Text)
	for _, tc := range c.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	n += len(c.ToolCallID)
	return n/4 + 4
}

func inspectTextTokens(s string) int {
	return len(s)/4 + 4
}

func inspectMessagePreview(m store.Message) string {
	if strings.TrimSpace(m.Content.Text) != "" {
		return fmt.Sprintf("text=%q", inspectTruncate(oneLine(m.Content.Text), 180))
	}
	if len(m.Content.ToolCalls) > 0 {
		names := make([]string, 0, len(m.Content.ToolCalls))
		for _, tc := range m.Content.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		return "tool_calls=" + strings.Join(names, ",")
	}
	if m.Content.ToolCallID != "" {
		return "tool_result_for=" + m.Content.ToolCallID
	}
	return "empty"
}

func inspectTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04:05 MST")
}

func inspectOptionalTime(t *time.Time) string {
	return inspectOptionalTimeIn(t, time.UTC)
}

func inspectOptionalTimeIn(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "-"
	}
	return inspectTime(*t, loc)
}

func inspectOptional(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func inspectAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < 48*time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Hour).String()
}

func inspectTruncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return strings.TrimSpace(s[:max-3]) + "..."
}

func inspectIndent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
