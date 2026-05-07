package notifier

import (
	"context"
	"fmt"
	"strings"

	"github.com/levmv/golems/hugin/internal/storage"
	"github.com/levmv/golems/pkg/telegram"
)

// Notifier sends alerts about incident events.
type Notifier interface {
	NotifyCreated(ctx context.Context, inc storage.IncidentRecord) error
	NotifyResolved(ctx context.Context, inc storage.IncidentRecord) error
	NotifyUpdated(ctx context.Context, inc storage.IncidentRecord) error
}

// Telegram sends incident alerts via Telegram Bot API.
type Telegram struct {
	bot    *telegram.Bot
	chatID int64
}

// NewTelegram creates a Telegram notifier. It validates the token on creation.
func NewTelegram(token string, chatID int64) (*Telegram, error) {
	bot, err := telegram.New(token, "")
	if err != nil {
		return nil, fmt.Errorf("telegram notifier: %w", err)
	}
	return &Telegram{bot: bot, chatID: chatID}, nil
}

func (t *Telegram) NotifyCreated(ctx context.Context, inc storage.IncidentRecord) error {
	msg := formatIncident(inc, "🔴 New Incident")
	return t.send(ctx, msg)
}

func (t *Telegram) NotifyResolved(ctx context.Context, inc storage.IncidentRecord) error {
	msg := formatIncident(inc, "🟢 Incident Resolved")
	return t.send(ctx, msg)
}

func (t *Telegram) NotifyUpdated(ctx context.Context, inc storage.IncidentRecord) error {
	msg := formatIncident(inc, "🟡 Incident Update")
	return t.send(ctx, msg)
}

func (t *Telegram) send(ctx context.Context, msg string) error {
	_, err := t.bot.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID: t.chatID,
		Text:   msg,
	})
	return err
}

func formatIncident(inc storage.IncidentRecord, header string) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Check: %s\n", inc.CheckID))
	b.WriteString(fmt.Sprintf("Severity: %s\n", inc.Severity))
	b.WriteString(fmt.Sprintf("Summary: %s\n", inc.Summary))
	b.WriteString(fmt.Sprintf("Incident ID: %s\n", inc.ID))
	return b.String()
}

// Log is a notifier that writes to a logger instead of sending real alerts.
// Useful for testing and dry-run mode.
type Log struct {
	Logf func(format string, args ...any)
}

func (l *Log) NotifyCreated(ctx context.Context, inc storage.IncidentRecord) error {
	l.Logf("NOTIFY CREATED: %s | %s | %s", inc.CheckID, inc.Severity, inc.Summary)
	return nil
}

func (l *Log) NotifyResolved(ctx context.Context, inc storage.IncidentRecord) error {
	l.Logf("NOTIFY RESOLVED: %s | %s", inc.ID, inc.CheckID)
	return nil
}

func (l *Log) NotifyUpdated(ctx context.Context, inc storage.IncidentRecord) error {
	l.Logf("NOTIFY UPDATED: %s | %s", inc.CheckID, inc.Severity)
	return nil
}
