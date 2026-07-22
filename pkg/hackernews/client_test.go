package hackernews

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/levmv/golems/pkg/webfetch"
)

func TestParseItemID(t *testing.T) {
	for _, input := range []string{"123", "https://news.ycombinator.com/item?id=123", "http://www.news.ycombinator.com/item?id=123&foo=bar"} {
		id, err := ParseItemID(input)
		if err != nil || id != 123 {
			t.Fatalf("ParseItemID(%q) = %d, %v", input, id, err)
		}
	}
	for _, input := range []string{"", "0", "https://example.com/item?id=123", "https://news.ycombinator.com/news", "file://news.ycombinator.com/item?id=123"} {
		if _, err := ParseItemID(input); err == nil {
			t.Errorf("ParseItemID(%q) unexpectedly succeeded", input)
		}
	}
}

func TestFeedPreservesRankedOrderAndCleansItems(t *testing.T) {
	client, closeServer := testClient(t, []int64{3, 1, 2}, map[int64]apiItem{
		1: {ID: 1, Type: "story", By: "alice", Time: 1_700_000_000, Title: "First &amp; <b>bold</b>", URL: "https://example.com/one", Score: 10, Descendants: 5},
		2: {ID: 2, Type: "story", By: "bob", Time: 1_700_000_100, Title: "Second", URL: "https://example.com/two", Score: 9},
		3: {ID: 3, Type: "story", Deleted: true},
	})
	defer closeServer()

	page, err := client.Feed(context.Background(), ViewTop, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Stories) != 2 || page.Stories[0].ID != 1 || page.Stories[1].ID != 2 {
		t.Fatalf("stories = %#v", page.Stories)
	}
	if page.Stories[0].Title != "First & bold" || page.Stories[0].URL != "https://example.com/one" {
		t.Fatalf("first story = %#v", page.Stories[0])
	}
}

func TestSearchUsesStoryScopeAndRequestedOrdering(t *testing.T) {
	for _, test := range []struct {
		name     string
		sort     SearchSort
		wantPath string
	}{
		{name: "relevance by default", wantPath: "/search"},
		{name: "newest first", sort: SearchSortDate, wantPath: "/search_by_date"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, closeServer := testSearchClient(t, searchResponse{Hits: []searchHit{
				{ObjectID: "123", Author: "alice", CreatedAt: 1_700_000_000, Title: "C++ &amp; SQLite", StoryText: "<p>Post text</p>", URL: "https://example.com/article", Points: 42, NumComments: 7},
				{ObjectID: "not-an-id", Title: "Invalid"},
			}}, func(r *http.Request) {
				if r.URL.Path != test.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, test.wantPath)
				}
				if r.URL.Query().Get("query") != "C++ SQLite" || r.URL.Query().Get("tags") != "story" || r.URL.Query().Get("hitsPerPage") != "2" {
					t.Errorf("query = %q", r.URL.RawQuery)
				}
			})
			defer closeServer()

			page, err := client.Search(context.Background(), "  C++ SQLite  ", test.sort, 2)
			if err != nil {
				t.Fatal(err)
			}
			if page.Query != "C++ SQLite" || len(page.Stories) != 1 {
				t.Fatalf("page = %#v", page)
			}
			story := page.Stories[0]
			if story.ID != 123 || story.Title != "C++ & SQLite" || story.Text != "Post text" || story.URL != "https://example.com/article" || story.Score != 42 || story.Comments != 7 {
				t.Fatalf("story = %#v", story)
			}
			if test.sort == "" && page.Sort != SearchSortRelevance {
				t.Fatalf("sort = %q, want relevance", page.Sort)
			}
		})
	}
}

func TestSearchRejectsMissingQueryAndUnknownSort(t *testing.T) {
	client := NewClient()
	if _, err := client.Search(context.Background(), " ", SearchSortRelevance, 10); err == nil {
		t.Fatal("empty query unexpectedly succeeded")
	}
	if _, err := client.Search(context.Background(), "Go", SearchSort("popular"), 10); err == nil {
		t.Fatal("unknown sort unexpectedly succeeded")
	}
}

func TestThreadLoadsBoundedRankedTree(t *testing.T) {
	client, closeServer := testClient(t, nil, map[int64]apiItem{
		100: {ID: 100, Type: "story", By: "poster", Time: 1_700_000_000, Title: "Topic", URL: "https://example.com/article", Score: 42, Descendants: 3, Kids: []int64{1, 2}},
		1:   {ID: 1, Type: "comment", By: "alice", Text: "<p>Hello <b>world</b></p>", Kids: []int64{3}},
		2:   {ID: 2, Type: "comment", By: "bob", Text: "Second root"},
		3:   {ID: 3, Type: "comment", By: "carol", Text: "Reply &amp; detail"},
	})
	defer closeServer()

	thread, err := client.Thread(context.Background(), "https://news.ycombinator.com/item?id=100")
	if err != nil {
		t.Fatal(err)
	}
	if thread.Story.ID != 100 || thread.Story.URL != "https://example.com/article" || len(thread.Roots) != 2 {
		t.Fatalf("thread = %#v", thread)
	}
	if thread.Roots[0].Text != "Hello world" || len(thread.Roots[0].Children) != 1 || thread.Roots[0].Children[0].Text != "Reply & detail" || thread.Roots[1].By != "bob" {
		t.Fatalf("comments = %#v", thread.Roots)
	}
	formatted := formatThread(thread)
	if !strings.Contains(formatted, "Article URL: https://example.com/article") || !strings.Contains(formatted, "  - carol [item 3]: Reply & detail") {
		t.Fatalf("formatted thread = %q", formatted)
	}
}

func TestThreadCapsTopLevelComments(t *testing.T) {
	items := map[int64]apiItem{}
	story := apiItem{ID: 100, Type: "story"}
	for id := int64(1); id <= maxTopLevelComments+5; id++ {
		story.Kids = append(story.Kids, id)
		items[id] = apiItem{ID: id, Type: "comment", By: "reader", Text: "comment"}
	}
	items[100] = story
	client, closeServer := testClient(t, nil, items)
	defer closeServer()
	thread, err := client.Thread(context.Background(), "100")
	if err != nil {
		t.Fatal(err)
	}
	if len(thread.Roots) != maxTopLevelComments {
		t.Fatalf("roots = %d, want %d", len(thread.Roots), maxTopLevelComments)
	}
}

func TestFetchBackendOnlyMatchesHNItemsAndDoesNotFetchArticle(t *testing.T) {
	var articleRequests atomic.Int32
	items := map[int64]apiItem{
		100: {ID: 100, Type: "story", Title: "Topic"},
	}
	client, closeServer := testClientWithHandler(t, nil, items, func(r *http.Request) {
		if r.URL.Path == "/article" {
			articleRequests.Add(1)
		}
	})
	items[100] = apiItem{ID: 100, Type: "story", Title: "Topic", URL: client.apiBase + "/article"}
	defer closeServer()
	backend := NewFetchBackend(client)
	if !backend.Match(webfetchRequest("https://news.ycombinator.com/item?id=100")) || backend.Match(webfetchRequest("https://example.com/article")) {
		t.Fatal("backend URL matching is incorrect")
	}
	result, err := webfetch.New(backend).Fetch(context.Background(), webfetchRequest("https://news.ycombinator.com/item?id=100"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != "hacker_news" || result.Title != "Topic" || !strings.Contains(result.Text, "Article URL: "+client.apiBase+"/article") || articleRequests.Load() != 0 {
		t.Fatalf("result=%#v article requests=%d", result, articleRequests.Load())
	}
}

func webfetchRequest(rawURL string) webfetch.Request {
	return webfetch.Request{URL: rawURL}
}

func testClient(t *testing.T, feed []int64, items map[int64]apiItem) (*Client, func()) {
	return testClientWithHandler(t, feed, items, nil)
}

func testClientWithHandler(t *testing.T, feed []int64, items map[int64]apiItem, observe func(*http.Request)) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observe != nil {
			observe(r)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/topstories.json" {
			_ = json.NewEncoder(w).Encode(feed)
			return
		}
		prefix := "/item/"
		if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, ".json") {
			http.NotFound(w, r)
			return
		}
		rawID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), ".json")
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			t.Errorf("invalid item request %q", r.URL.Path)
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		item, ok := items[id]
		if !ok {
			_ = json.NewEncoder(w).Encode(nil)
			return
		}
		_ = json.NewEncoder(w).Encode(item)
	}))
	client := NewClient()
	client.apiBase = server.URL
	client.client = server.Client()
	return client, server.Close
}

func testSearchClient(t *testing.T, result searchResponse, observe func(*http.Request)) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observe != nil {
			observe(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	client := NewClient()
	client.searchBase = server.URL
	client.client = server.Client()
	return client, server.Close
}
