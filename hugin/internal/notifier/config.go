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
		if token != "" && chatIDStr != "" {
			chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
			if err == nil {
				ntf, err := NewTelegram(token, chatID)
				if err != nil {
					log.Error("Failed to create Telegram notifier: %v", err)
				} else {
					return ntf
				}
			}
		}
	}

	return &Log{Logf: log.Info}
}
