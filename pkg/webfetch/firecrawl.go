package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const firecrawlScrapeEndpoint = "https://api.firecrawl.dev/v2/scrape"

type FirecrawlBackend struct {
	token    string
	endpoint string
	client   *http.Client
}

func NewFirecrawlBackend(token string) *FirecrawlBackend {
	return &FirecrawlBackend{
		token:    strings.TrimSpace(token),
		endpoint: firecrawlScrapeEndpoint,
		client:   &http.Client{Timeout: 55 * time.Second},
	}
}

func (*FirecrawlBackend) Name() string { return "firecrawl" }

func (b *FirecrawlBackend) Fetch(ctx context.Context, request Request) (Result, error) {
	body, err := json.Marshal(struct {
		URL             string   `json:"url"`
		Formats         []string `json:"formats"`
		OnlyMainContent bool     `json:"onlyMainContent"`
		Proxy           string   `json:"proxy"`
		Timeout         int      `json:"timeout"`
	}{
		URL:             request.URL,
		Formats:         []string{"markdown"},
		OnlyMainContent: true,
		Proxy:           "basic",
		Timeout:         45_000,
	})
	if err != nil {
		return Result{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+b.token)
	response, err := b.client.Do(httpRequest)
	if err != nil {
		return Result{}, fmt.Errorf("request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return Result{}, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Markdown string `json:"markdown"`
			Metadata struct {
				Title     string `json:"title"`
				SourceURL string `json:"sourceURL"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	if !payload.Success {
		message := compactText(payload.Error, 240)
		if message == "" {
			message = "scrape was unsuccessful"
		}
		return Result{}, errors.New(message)
	}
	resultURL := payload.Data.Metadata.SourceURL
	if strings.TrimSpace(resultURL) == "" {
		resultURL = request.URL
	}
	return Result{
		URL:   resultURL,
		Title: payload.Data.Metadata.Title,
		Text:  payload.Data.Markdown,
	}, nil
}
