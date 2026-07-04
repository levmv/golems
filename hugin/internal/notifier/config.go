package notifier

import (
	"os"
	"strconv"

	"github.com/levmv/golems/hugin/internal/config"
	"github.com/levmv/golems/pkg/logger"
)

func FromConfig(cfg *config.Config, log logger.Logger) Notifier {
	if tg, ok := cfg.Notifiers["telegram"]; ok && tg.Enabled {
		token := os.Getenv(tg.BotTokenEnv)
		chatIDStr := os.Getenv(tg.ChatIDEnv)
		if token == "" {
			log.Error("Telegram notifier is enabled but env %s is empty; falling back to log notifier", tg.BotTokenEnv)
			return &Log{Logf: log.Info}
		}
		if chatIDStr == "" {
			log.Error("Telegram notifier is enabled but env %s is empty; falling back to log notifier", tg.ChatIDEnv)
			return &Log{Logf: log.Info}
		}
		chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
		if err != nil {
			log.Error("Telegram notifier chat id env %s is invalid: %v; falling back to log notifier", tg.ChatIDEnv, err)
			return &Log{Logf: log.Info}
		}
		ntf, err := NewTelegram(token, chatID)
		if err != nil {
			log.Error("Failed to create Telegram notifier: %v; falling back to log notifier", err)
		} else {
			return ntf
		}
	}

	return &Log{Logf: log.Info}
}
