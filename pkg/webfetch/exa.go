package webfetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const exaContentsEndpoint = "https://api.exa.ai/contents"

type ExaBackend struct {
	token    string
	endpoint string
	client   *http.Client
}

func NewExaBackend(token string) *ExaBackend {
	return &ExaBackend{
		token:    strings.TrimSpace(token),
		endpoint: exaContentsEndpoint,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (*ExaBackend) Name() string { return "exa" }

func (b *ExaBackend) Fetch(ctx context.Context, request Request) (Result, error) {
	body, err := json.Marshal(struct {
		URLs []string `json:"urls"`
		Text struct {
			MaxCharacters int `json:"maxCharacters"`
		} `json:"text"`
		MaxAgeHours      int `json:"maxAgeHours"`
		LivecrawlTimeout int `json:"livecrawlTimeout"`
	}{
		URLs: []string{request.URL},
		Text: struct {
			MaxCharacters int `json:"maxCharacters"`
		}{MaxCharacters: maxTextBytes},
		MaxAgeHours:      24,
		LivecrawlTimeout: 12_000,
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
	httpRequest.Header.Set("x-api-key", b.token)
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
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Text  string `json:"text"`
		} `json:"results"`
		Statuses []struct {
			Status string `json:"status"`
			Error  struct {
				Tag string `json:"tag"`
			} `json:"error"`
		} `json:"statuses"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("decode response: %w", err)
	}
	if len(payload.Results) == 0 {
		for _, status := range payload.Statuses {
			if status.Status == "error" && status.Error.Tag != "" {
				return Result{}, fmt.Errorf("contents: %s", compactText(status.Error.Tag, 120))
			}
		}
		return Result{}, fmt.Errorf("contents returned no result")
	}
	result := payload.Results[0]
	resultURL := result.URL
	if strings.TrimSpace(resultURL) == "" {
		resultURL = request.URL
	}
	return Result{
		URL:   resultURL,
		Title: result.Title,
		Text:  result.Text,
	}, nil
}
