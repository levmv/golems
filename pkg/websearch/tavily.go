package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const tavilyEndpoint = "https://api.tavily.com/search"

type tavilyProvider struct {
	token    string
	endpoint string
	client   *http.Client
}

func newTavilyProvider(token string) *tavilyProvider {
	return &tavilyProvider{token: token, endpoint: tavilyEndpoint, client: &http.Client{Timeout: providerTimeout}}
}

func (*tavilyProvider) Name() string { return "tavily" }

func (p *tavilyProvider) Search(ctx context.Context, request Request) ([]Result, error) {
	body, err := json.Marshal(struct {
		Query       string `json:"query"`
		SearchDepth string `json:"search_depth"`
		MaxResults  int    `json:"max_results"`
	}{Query: request.Query, SearchDepth: "basic", MaxResults: request.Limit})
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+p.token)
	response, err := p.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	results := make([]Result, 0, min(request.Limit, len(payload.Results)))
	for _, result := range payload.Results {
		results = append(results, Result{Title: result.Title, URL: result.URL, Snippet: result.Content})
	}
	return results, nil
}
