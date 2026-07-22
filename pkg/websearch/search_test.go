package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

func TestTavilySearchIsBoundedAndMarksContentUntrusted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer search-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Query       string `json:"query"`
			SearchDepth string `json:"search_depth"`
			MaxResults  int    `json:"max_results"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Query != "durable agents" || request.SearchDepth != "basic" || request.MaxResults != 1 {
			t.Fatalf("request payload = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{
			{"title": "First", "url": "https://example.com/one\x1b[31m", "content": "one snippet"},
			{"title": "Second", "url": "https://example.com/two", "content": "two snippet"},
		}})
	}))
	defer server.Close()
	provider := newTavilyProvider("search-secret")
	provider.endpoint = server.URL + "/search"
	provider.client = server.Client()
	searcher := New(provider)
	tool := Tool(searcher)
	result, err := tool.Run(context.Background(), llm.ToolCall{Function: llm.ToolFunction{Arguments: `{"query":"durable agents","limit":1}`}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "UNTRUSTED WEB SEARCH RESULTS") || !strings.Contains(result.Content, "provider: tavily") || !strings.Contains(result.Content, "First") || strings.Contains(result.Content, "Second") || strings.Contains(result.Content, "search-secret") || strings.Contains(result.Content, "\x1b") {
		t.Fatalf("search result = %q", result.Content)
	}
}

func TestExaSearchRequestAndResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "exa-secret" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		var request struct {
			Query      string `json:"query"`
			NumResults int    `json:"numResults"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Query != "semantic search" || request.NumResults != 2 {
			t.Fatalf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"title": "Exa result", "url": "https://exa.example/result", "text": "optional text"}}})
	}))
	defer server.Close()
	provider := newExaProvider("exa-secret")
	provider.endpoint = server.URL
	provider.client = server.Client()
	results, err := provider.Search(context.Background(), Request{Query: "semantic search", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "Exa result" || results[0].Snippet != "optional text" {
		t.Fatalf("results = %#v", results)
	}
}

func TestSearchFallsBackSequentially(t *testing.T) {
	first := &providerStub{name: "first", err: errors.New("HTTP 429")}
	second := &providerStub{name: "second"}
	third := &providerStub{name: "third", results: []Result{{Title: "Found", URL: "https://example.com", Snippet: "result"}}}
	fourth := &providerStub{name: "fourth", results: []Result{{Title: "unused", URL: "https://example.net"}}}
	searcher := New(first, second, third, fourth)
	results, meta, err := searcher.Search(context.Background(), Request{Query: "fallback"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || meta.Provider != "third" {
		t.Fatalf("results=%#v meta=%#v", results, meta)
	}
	if first.calls != 1 || second.calls != 1 || third.calls != 1 || fourth.calls != 0 {
		t.Fatalf("provider calls = %d, %d, %d, %d", first.calls, second.calls, third.calls, fourth.calls)
	}
}

func TestToolRequiresCredentialAndSupportsBothProviders(t *testing.T) {
	if _, ok, err := NewTool(nil); err != nil || ok {
		t.Fatalf("empty search tool: ok=%v err=%v", ok, err)
	}
	tool, ok, err := NewTool([]Credential{{Provider: "tavily", Token: "token"}, {Provider: "exa", Token: "token"}})
	if err != nil || !ok || tool.Definition.Function.Name != "web_search" || tool.Effect != golem.ToolEffectExternal {
		t.Fatalf("configured search tool = %#v, %v, %v", tool, ok, err)
	}
}

func TestSearchDropsInvalidAndDuplicateResultURLs(t *testing.T) {
	provider := &providerStub{name: "test", results: []Result{
		{Title: "file", URL: "file:///etc/passwd"},
		{Title: "credentials", URL: "https://user:pass@example.com/private"},
		{Title: "public", URL: "https://example.com/page"},
		{Title: "duplicate", URL: "https://example.com/page"},
	}}
	searcher := New(provider)
	results, _, err := searcher.Search(context.Background(), Request{Query: "safe results"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "public" {
		t.Fatalf("results = %#v", results)
	}
}

type providerStub struct {
	name    string
	results []Result
	err     error
	calls   int
}

func (p *providerStub) Name() string { return p.name }

func (p *providerStub) Search(context.Context, Request) ([]Result, error) {
	p.calls++
	return p.results, p.err
}
