package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/logger"
)

type TelegramConfig struct {
	Token        string   `json:"token"`
	AllowedUsers []string `json:"allowed_users"`
}

type BotConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`

	Model        string  `json:"model"`
	SystemPrompt string  `json:"system_prompt"`
	Temperature  float64 `json:"temperature"`

	Telegram *TelegramConfig `json:"telegram,omitempty"`
	// Map: target chat ID -> admin/control channel ID
	ControlChats map[string]string `json:"control_chats"`
}

func LoadConfig(path string) ([]BotConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read config file: %w", err)
	}

	var bots []BotConfig
	if err := json.Unmarshal(data, &bots); err != nil {
		return nil, fmt.Errorf("could not parse config JSON: %w", err)
	}
	return bots, nil
}

func startBot(ctx context.Context, cfg BotConfig, r *llm.Registry, dataDir string, baseWebhookURL *url.URL, mux *http.ServeMux) {
	model, err := r.Model(cfg.Model)
	if err != nil {
		Log.Error("Failed to init model %s: %v", cfg.Model, err)
		return
	}
	model = model.
		WithRetries(2, time.Second).
		WithTemperature(float32(cfg.Temperature))

	storage := NewStorage(filepath.Join(dataDir, cfg.ID))
	registry := NewSessionRegistry(storage, &model)
	engine := NewEngine(registry, &model, cfg.Name, cfg.SystemPrompt, cfg.ControlChats)

	if cfg.Telegram != nil {
		var webhookURL, webhookPath string
		if baseWebhookURL != nil {
			u := baseWebhookURL.JoinPath(cfg.ID)
			webhookURL = u.String()
			webhookPath = u.Path
		}

		tgGateway, handler, err := StartTelegramBot(ctx, cfg.Telegram, engine, webhookURL)
		if err != nil {
			Log.Error("Failed to start Telegram gateway for bot %s: %v", cfg.ID, err)
			return
		}

		engine.RegisterGateway("tg", tgGateway)
		if handler != nil {
			mux.Handle(webhookPath, handler)
		}
		Log.Info("Telegram gateway started for %s", cfg.ID)
	}

	go engine.StartBackgroundObserver(ctx)
}

var Log = logger.Default()

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	configPath := flag.String("config", "config.json", "path to configuration file")
	debugFlag := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	if *debugFlag || os.Getenv("DEBUG") == "1" || os.Getenv("DEBUG") == "true" {
		Log = logger.New(logger.Config{Level: logger.LevelDebug})
		Log.Debug("Debug logging enabled")
	}

	bots, err := LoadConfig(*configPath)
	if err != nil {
		Log.Error("Failed to load config: %v", err)
		os.Exit(1)
	}

	dataDir := cmp.Or(os.Getenv("DATA_DIR"), "bots")
	mux := http.NewServeMux()

	rawBaseURL := os.Getenv("WEBHOOK_BASE_URL")
	var baseWebhookURL *url.URL

	if rawBaseURL != "" {
		baseWebhookURL, err = url.Parse(rawBaseURL)
		if err != nil {
			Log.Error("Invalid WEBHOOK_BASE_URL configuration: %v", err)
			os.Exit(1)
		}
	}

	r := llm.NewRegistry().
		WithProvider("deepseek", os.Getenv("DEEPSEEK_API_KEY")).
		WithProvider("openrouter", os.Getenv("OPENROUTER_API_KEY"))

	for _, botCfg := range bots {
		startBot(ctx, botCfg, r, dataDir, baseWebhookURL, mux)
	}

	var srv *http.Server
	if baseWebhookURL != nil {
		listenAddr := ":" + cmp.Or(os.Getenv("PORT"), "8443")
		srv = &http.Server{Addr: listenAddr, Handler: mux}

		Log.Info("Starting HTTP server for Webhooks on %s", listenAddr)
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				Log.Error("HTTP server error: %v", err)
				os.Exit(1)
			}
		}()
	}

	Log.Info("All systems active. Press Ctrl+C to stop.")
	<-ctx.Done()

	Log.Info("Shutting down gracefully...")
	if srv != nil {
		shutdownCtx, srvCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer srvCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			Log.Error("HTTP shutdown error: %v", err)
		}
	}
}
