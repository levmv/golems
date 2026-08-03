package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	sourcefetch "github.com/levmv/golems/brevity/internal/fetch"
	"github.com/levmv/golems/brevity/internal/resolve"
	"github.com/levmv/golems/pkg/llm"
	"github.com/levmv/golems/pkg/logger"
)

const (
	defaultModelURI       = "deepseek/deepseek-v4-flash"
	modelTemperature      = 0.2
	modelMaxTokens        = 6500
	telegraphHTTPTimeout  = 25 * time.Second
	useJinaReaderFallback = true
)

// Set these while experimenting with external readers.
// externalFetcherCommand is a plain text stdout reader.
// browserFetcherCommand returns the JSON contract documented in README.
var externalFetcherCommand []string
var browserFetcherCommand []string

func main() {
	createTelegraphAccount := flag.Bool("create-telegraph-account", false, "create a Telegraph account and print its access token")
	telegraphShortName := flag.String("telegraph-short-name", "Brevity", "short_name for -create-telegraph-account")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := LoadConfig()
	log := logger.Default()
	if cfg.Debug {
		log = logger.New(logger.Config{Level: logger.LevelDebug})
	}

	if *createTelegraphAccount {
		account, err := NewTelegraphClient("", cfg.TelegraphAuthorName, cfg.TelegraphAuthorURL, telegraphHTTPTimeout).
			CreateAccount(ctx, *telegraphShortName)
		if err != nil {
			log.Error("failed to create Telegraph account: %v", err)
			os.Exit(1)
		}
		fmt.Printf("TELEGRAPH_ACCESS_TOKEN=%s\n", account.AccessToken)
		if account.AuthURL != "" {
			fmt.Printf("TELEGRAPH_AUTH_URL=%s\n", account.AuthURL)
		}
		return
	}

	if cfg.TelegramBotToken == "" {
		log.Error("TELEGRAM_BOT_TOKEN is required")
		os.Exit(1)
	}
	if len(cfg.TelegramWhitelist) == 0 {
		log.Warn("TELEGRAM_WHITELIST is empty; /start will show user IDs but no one is allowed yet")
	}

	model, err := buildModel(cfg)
	if err != nil {
		log.Error("failed to initialize model: %v", err)
		os.Exit(1)
	}

	var publisher Publisher
	if cfg.TelegraphAccessToken != "" {
		publisher = NewTelegraphClient(cfg.TelegraphAccessToken, cfg.TelegraphAuthorName, cfg.TelegraphAuthorURL, telegraphHTTPTimeout)
	} else {
		log.Warn("TELEGRAPH_ACCESS_TOKEN is empty; full summaries will be sent back to Telegram instead of Telegraph")
	}

	service := NewService(
		buildResolver(),
		NewLLMSummarizer(model),
		publisher,
	)

	adapter, err := NewTelegramAdapter(cfg, service, log)
	if err != nil {
		log.Error("failed to initialize Telegram adapter: %v", err)
		os.Exit(1)
	}

	if err = adapter.Start(ctx, cfg); err != nil {
		log.Error("Brevity stopped with error: %v", err)
		os.Exit(1)
	}
}

func buildResolver() Resolver {
	defaultResolver := resolve.NewDefault(buildFetcher())
	return resolve.NewChain(defaultResolver, resolve.NewHN(defaultResolver))
}

func buildFetcher() sourcefetch.Fetcher {
	var fetcher sourcefetch.Fetcher = sourcefetch.NewHTTP(nil)
	if useJinaReaderFallback {
		fetcher = sourcefetch.WithFallback(fetcher, sourcefetch.NewJina())
	}
	if len(externalFetcherCommand) > 0 {
		secondary := sourcefetch.NewCommand(externalFetcherCommand[0], externalFetcherCommand[1:]...)
		fetcher = sourcefetch.WithFallback(fetcher, secondary)
	}
	if len(browserFetcherCommand) > 0 {
		secondary := sourcefetch.NewBrowserCommand(browserFetcherCommand[0], browserFetcherCommand[1:]...)
		fetcher = sourcefetch.WithFallback(fetcher, secondary)
	}
	return fetcher
}

func buildModel(cfg Config) (llm.Model, error) {
	registry := llm.NewRegistry().
		WithProvider("deepseek", os.Getenv("DEEPSEEK_API_KEY")).
		WithProvider("openai", os.Getenv("OPENAI_API_KEY")).
		WithProvider("openrouter", os.Getenv("OPENROUTER_API_KEY"), llm.WithAppAttribution("Brevity", "https://github.com/levmv/golems")).
		WithProvider("ollama", "ollama")

	provider := modelProvider(cfg.ModelURI)
	if provider != "ollama" && providerToken(provider) == "" {
		return llm.Model{}, fmt.Errorf("%s API key is empty for model %q", provider, cfg.ModelURI)
	}

	model, err := registry.Model(cfg.ModelURI)
	if err != nil {
		return llm.Model{}, err
	}
	return model.
		WithRetries(2, time.Second).
		WithTemperature(modelTemperature).
		WithMaxTokens(modelMaxTokens), nil
}

func modelProvider(uri string) string {
	provider, _, ok := strings.Cut(uri, "/")
	if !ok {
		return ""
	}
	return provider
}

func providerToken(provider string) string {
	switch provider {
	case "deepseek":
		return os.Getenv("DEEPSEEK_API_KEY")
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "openrouter":
		return os.Getenv("OPENROUTER_API_KEY")
	default:
		return ""
	}
}
