package main

import (
	"cmp"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	TelegramBotToken     string
	TelegramWhitelist    []int64
	WebhookURL           string
	WebhookSecretToken   string
	Port                 string
	Debug                bool
	ModelURI             string
	TelegraphAccessToken string
	TelegraphAuthorName  string
	TelegraphAuthorURL   string
}

func LoadConfig() Config {
	return Config{
		TelegramBotToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramWhitelist:    parseInt64List(os.Getenv("TELEGRAM_WHITELIST")),
		WebhookURL:           os.Getenv("WEBHOOK_URL"),
		WebhookSecretToken:   os.Getenv("WEBHOOK_SECRET_TOKEN"),
		Port:                 cmp.Or(os.Getenv("PORT"), "8443"),
		Debug:                parseBool(os.Getenv("DEBUG")),
		ModelURI:             cmp.Or(os.Getenv("BREVITY_MODEL"), defaultModelURI),
		TelegraphAccessToken: os.Getenv("TELEGRAPH_ACCESS_TOKEN"),
		TelegraphAuthorName:  cmp.Or(os.Getenv("TELEGRAPH_AUTHOR_NAME"), "Brevity"),
		TelegraphAuthorURL:   os.Getenv("TELEGRAPH_AUTHOR_URL"),
	}
}

func parseInt64List(env string) []int64 {
	var out []int64
	for p := range strings.SplitSeq(env, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func parseBool(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
