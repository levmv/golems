package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const openRouterModelsURL = "https://openrouter.ai/api/v1"

var openRouterMetadataClient = &http.Client{Timeout: 2 * time.Second}

func openRouterContextWindow(modelID string) (int, error) {
	return fetchOpenRouterContextWindow(openRouterMetadataClient, openRouterModelsURL, modelID)
}

func fetchOpenRouterContextWindow(client *http.Client, baseURL, modelID string) (int, error) {
	parts := strings.Split(strings.TrimSpace(modelID), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("invalid OpenRouter model ID %q", modelID)
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/model/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Cy")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("OpenRouter model metadata: %s", resp.Status)
	}
	var payload struct {
		Data struct {
			ContextLength int `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode OpenRouter model metadata: %w", err)
	}
	if payload.Data.ContextLength <= 0 {
		return 0, errors.New("OpenRouter model metadata has no context length")
	}
	return payload.Data.ContextLength, nil
}
