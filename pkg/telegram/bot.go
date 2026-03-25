package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout    = time.Minute
	defaultUpdatesCap = 1024
	checkInitTimeout  = time.Second * 5
)

type HttpClient interface {
	Do(*http.Request) (*http.Response, error)
}

type MatchFunc func(update *Update) bool

type AuthFunc func(*Context) (allow bool, reason string)

type Logger interface {
	Debug(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
}

type defaultLogger struct{}

func (l defaultLogger) Debug(format string, args ...any) { log.Printf("[TG][DEBUG] "+format, args...) }
func (l defaultLogger) Info(format string, args ...any)  { log.Printf("[TG][INFO] "+format, args...) }
func (l defaultLogger) Warn(format string, args ...any)  { log.Printf("[TG][WARN] "+format, args...) }
func (l defaultLogger) Error(format string, args ...any) { log.Printf("[TG][ERROR] "+format, args...) }

type Bot struct {
	token              string
	url                string
	webhookSecretToken string

	Me *User

	authFunc AuthFunc

	handlers []route

	client  HttpClient
	isDebug bool

	logger Logger

	updates chan *Update

	// Per-chat routing
	chatQueuesMx sync.Mutex
	chatQueues   map[int64]chan *Context
}

func New(token string, webhookSecretToken string, options ...Option) (*Bot, error) {
	if !strings.Contains(token, ":") {
		return nil, errors.New("invalid bot token")
	}

	b := &Bot{
		url:                "https://api.telegram.org",
		token:              token,
		webhookSecretToken: webhookSecretToken,
		client:             &http.Client{Timeout: defaultTimeout},
		logger:             defaultLogger{},
		chatQueues:         make(map[int64]chan *Context),
	}

	for _, o := range options {
		o(b)
	}

	if b.updates == nil {
		b.updates = make(chan *Update, defaultUpdatesCap)
	}

	ctx, cancel := context.WithTimeout(context.Background(), checkInitTimeout)
	defer cancel()

	me, err := b.GetMe(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get bot info: %w", err)
	}

	b.Me = me

	return b, nil
}

type Option func(b *Bot)

func WithDebug() Option {
	return func(b *Bot) {
		b.isDebug = true
	}
}

func WithLogger(logger Logger) Option {
	return func(b *Bot) {
		b.logger = logger
	}
}

func WithHTTPClient(client HttpClient) Option {
	return func(b *Bot) {
		b.client = client
	}
}

func WithServerURL(url string) Option {
	return func(b *Bot) {
		b.url = url
	}
}

func WithAuthFunc(f AuthFunc) Option {
	return func(b *Bot) { b.authFunc = f }
}
