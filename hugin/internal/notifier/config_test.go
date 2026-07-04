package notifier

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/pkg/logger"
)

func TestFromConfigLogsTelegramFallback(t *testing.T) {
	t.Setenv("HUGIN_TEST_TELEGRAM_TOKEN", "token")
	t.Setenv("HUGIN_TEST_TELEGRAM_CHAT", "not-an-int")

	useColors := false
	var stderr bytes.Buffer
	log := logger.New(logger.Config{Out: io.Discard, Err: &stderr, UseColors: &useColors})
	ntf := FromConfig(&config.Config{
		Notifiers: map[string]config.Notifier{
			"telegram": {
				Enabled:     true,
				BotTokenEnv: "HUGIN_TEST_TELEGRAM_TOKEN",
				ChatIDEnv:   "HUGIN_TEST_TELEGRAM_CHAT",
			},
		},
	}, log)

	if _, ok := ntf.(*Log); !ok {
		t.Fatalf("expected log notifier fallback, got %T", ntf)
	}
	if !strings.Contains(stderr.String(), "invalid") {
		t.Fatalf("expected fallback reason in error log, got %q", stderr.String())
	}
}
