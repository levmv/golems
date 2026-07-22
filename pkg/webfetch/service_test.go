package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFirecrawlBackendUsesBoundedBasicMarkdownScrape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fc-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			URL             string   `json:"url"`
			Formats         []string `json:"formats"`
			OnlyMainContent bool     `json:"onlyMainContent"`
			Proxy           string   `json:"proxy"`
			Timeout         int      `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.URL != "https://example.com/article" || len(request.Formats) != 1 || request.Formats[0] != "markdown" || !request.OnlyMainContent || request.Proxy != "basic" || request.Timeout != 45_000 {
			t.Fatalf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": "# Clean page\n\nBody",
				"metadata": map[string]any{"title": "Clean page", "sourceURL": "https://example.com/article", "statusCode": 200, "contentType": "text/html"},
			},
		})
	}))
	defer server.Close()
	backend := NewFirecrawlBackend("fc-secret")
	backend.endpoint = server.URL
	backend.client = server.Client()
	result, err := backend.Fetch(context.Background(), Request{URL: "https://example.com/article"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Clean page" || result.Text != "# Clean page\n\nBody" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExaBackendRequestsCleanBoundedText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "exa-secret" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		var request struct {
			URLs []string `json:"urls"`
			Text struct {
				MaxCharacters int `json:"maxCharacters"`
			} `json:"text"`
			MaxAgeHours      int `json:"maxAgeHours"`
			LivecrawlTimeout int `json:"livecrawlTimeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.URLs) != 1 || request.URLs[0] != "https://example.com/article" || request.Text.MaxCharacters != maxTextBytes || request.MaxAgeHours != 24 || request.LivecrawlTimeout != 12_000 {
			t.Fatalf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"title": "Exa page", "url": "https://example.com/article", "text": "Clean Exa text"}}})
	}))
	defer server.Close()
	backend := NewExaBackend("exa-secret")
	backend.endpoint = server.URL
	backend.client = server.Client()
	result, err := backend.Fetch(context.Background(), Request{URL: "https://example.com/article"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Exa page" || result.Text != "Clean Exa text" {
		t.Fatalf("result = %#v", result)
	}
}
