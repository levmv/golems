// Package hackernews provides bounded access to Hacker News feeds,
// full-text search, discussions, and an adapter for webfetch.
package hackernews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBaseURL            = "https://hacker-news.firebaseio.com/v0"
	searchBaseURL         = "https://hn.algolia.com/api/v1"
	requestTimeout        = 20 * time.Second
	maxResponseBytes      = 1 * 1024 * 1024
	defaultStoryLimit     = 10
	maxStoryLimit         = 30
	maxTopLevelComments   = 15
	maxCommentDepth       = 3
	maxComments           = 45
	maxCommentCharacters  = 1200
	maxConcurrentRequests = 8
)

type View string

const (
	ViewTop    View = "top"
	ViewNew    View = "new"
	ViewBest   View = "best"
	ViewShow   View = "show"
	ViewSearch View = "search"
	ViewThread View = "thread"
)

type SearchSort string

const (
	SearchSortRelevance SearchSort = "relevance"
	SearchSortDate      SearchSort = "date"
)

type Story struct {
	ID          int64
	Type        string
	By          string
	PublishedAt time.Time
	Title       string
	Text        string
	URL         string
	Score       int
	Comments    int
}

type Comment struct {
	ID       int64
	By       string
	Text     string
	Children []*Comment
}

type FeedPage struct {
	View    View
	Stories []Story
	Warning string
}

type SearchPage struct {
	Query   string
	Sort    SearchSort
	Stories []Story
}

type Thread struct {
	Story   Story
	Roots   []*Comment
	Warning string
}

type Client struct {
	apiBase    string
	searchBase string
	client     *http.Client
}

func NewClient() *Client {
	return &Client{
		apiBase:    apiBaseURL,
		searchBase: searchBaseURL,
		client:     &http.Client{Timeout: requestTimeout},
	}
}

func (c *Client) Feed(ctx context.Context, view View, limit int) (FeedPage, error) {
	endpoint, err := feedEndpoint(view)
	if err != nil {
		return FeedPage{}, err
	}
	if limit <= 0 {
		limit = defaultStoryLimit
	}
	limit = min(limit, maxStoryLimit)

	ids, err := c.fetchIDs(ctx, endpoint)
	if err != nil {
		return FeedPage{}, err
	}
	candidateCount := min(len(ids), min(max(limit*2, limit+5), maxStoryLimit*2))
	results := c.fetchItems(ctx, ids[:candidateCount])
	if err := ctx.Err(); err != nil {
		return FeedPage{}, err
	}
	page := FeedPage{View: view, Stories: make([]Story, 0, limit)}
	failed := 0
	var firstErr error
	for _, result := range results {
		if result.err != nil {
			failed++
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		item := result.item
		if item.Deleted || item.Dead || !isStory(item.Type) {
			continue
		}
		page.Stories = append(page.Stories, storyFromItem(item))
		if len(page.Stories) == limit {
			break
		}
	}
	if len(page.Stories) == 0 && firstErr != nil {
		return FeedPage{}, fmt.Errorf("load Hacker News feed items: %w", firstErr)
	}
	if failed > 0 {
		page.Warning = fmt.Sprintf("%d feed item request(s) failed; first error: %v", failed, firstErr)
	}
	return page, nil
}

func (c *Client) Thread(ctx context.Context, itemReference string) (Thread, error) {
	id, err := ParseItemID(itemReference)
	if err != nil {
		return Thread{}, err
	}
	item, err := c.fetchItem(ctx, id)
	if err != nil {
		return Thread{}, err
	}
	if item.Deleted || item.Dead {
		return Thread{}, fmt.Errorf("Hacker News item %d is deleted or dead", id)
	}
	if !isStory(item.Type) {
		return Thread{}, fmt.Errorf("Hacker News item %d is a %s, not a discussion", id, item.Type)
	}
	roots, warning := c.collectComments(ctx, item.Kids)
	if err := ctx.Err(); err != nil {
		return Thread{}, err
	}
	return Thread{Story: storyFromItem(item), Roots: roots, Warning: warning}, nil
}

func (c *Client) Search(ctx context.Context, query string, sort SearchSort, limit int) (SearchPage, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchPage{}, errors.New("Hacker News search query is required")
	}
	var endpoint string
	switch sort {
	case "", SearchSortRelevance:
		sort = SearchSortRelevance
		endpoint = "search"
	case SearchSortDate:
		endpoint = "search_by_date"
	default:
		return SearchPage{}, fmt.Errorf("unsupported Hacker News search sort %q", sort)
	}
	if limit <= 0 {
		limit = defaultStoryLimit
	}
	limit = min(limit, maxStoryLimit)

	target, err := url.Parse(strings.TrimRight(c.searchBase, "/") + "/" + endpoint)
	if err != nil {
		return SearchPage{}, fmt.Errorf("build Hacker News search URL: %w", err)
	}
	parameters := target.Query()
	parameters.Set("query", query)
	parameters.Set("tags", "story")
	parameters.Set("hitsPerPage", strconv.Itoa(limit))
	target.RawQuery = parameters.Encode()

	var result searchResponse
	if err := c.getJSON(ctx, target.String(), "Hacker News search", &result); err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Query: query, Sort: sort, Stories: make([]Story, 0, min(len(result.Hits), limit))}
	for _, hit := range result.Hits {
		story, ok := storyFromSearchHit(hit)
		if !ok {
			continue
		}
		page.Stories = append(page.Stories, story)
		if len(page.Stories) == limit {
			break
		}
	}
	return page, nil
}

func ParseItemID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
		return id, nil
	}
	target, err := url.Parse(raw)
	if err != nil || target.Hostname() == "" {
		return 0, errors.New("Hacker News item must be a positive ID or item URL")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return 0, errors.New("Hacker News item URL must use http or https")
	}
	host := strings.TrimPrefix(strings.ToLower(target.Hostname()), "www.")
	if host != "news.ycombinator.com" || strings.Trim(target.Path, "/") != "item" {
		return 0, errors.New("Hacker News item URL must use news.ycombinator.com/item")
	}
	id, err := strconv.ParseInt(target.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("Hacker News item URL has no valid id")
	}
	return id, nil
}

func ItemURL(id int64) string {
	return fmt.Sprintf("https://news.ycombinator.com/item?id=%d", id)
}

func normalizeView(raw string) (View, error) {
	view := View(strings.ToLower(strings.TrimSpace(raw)))
	switch view {
	case ViewTop, ViewNew, ViewBest, ViewShow, ViewSearch, ViewThread:
		return view, nil
	default:
		return "", fmt.Errorf("unsupported Hacker News view %q", raw)
	}
}

func feedEndpoint(view View) (string, error) {
	switch view {
	case ViewTop:
		return "topstories", nil
	case ViewNew:
		return "newstories", nil
	case ViewBest:
		return "beststories", nil
	case ViewShow:
		return "showstories", nil
	default:
		return "", fmt.Errorf("Hacker News view %q is not a feed", view)
	}
}

type searchResponse struct {
	Hits []searchHit `json:"hits"`
}

type searchHit struct {
	ObjectID    string `json:"objectID"`
	Author      string `json:"author"`
	CreatedAt   int64  `json:"created_at_i"`
	Title       string `json:"title"`
	StoryText   string `json:"story_text"`
	URL         string `json:"url"`
	Points      int    `json:"points"`
	NumComments int    `json:"num_comments"`
}

type apiItem struct {
	ID          int64   `json:"id"`
	Deleted     bool    `json:"deleted"`
	Type        string  `json:"type"`
	By          string  `json:"by"`
	Time        int64   `json:"time"`
	Text        string  `json:"text"`
	Dead        bool    `json:"dead"`
	Kids        []int64 `json:"kids"`
	URL         string  `json:"url"`
	Score       int     `json:"score"`
	Title       string  `json:"title"`
	Descendants int     `json:"descendants"`
}

type itemResult struct {
	item apiItem
	err  error
}

func isStory(itemType string) bool {
	return itemType == "story" || itemType == "job" || itemType == "poll"
}

func (c *Client) fetchIDs(ctx context.Context, endpoint string) ([]int64, error) {
	var ids []int64
	if err := c.getJSON(ctx, strings.TrimRight(c.apiBase, "/")+"/"+endpoint+".json", "Hacker News feed", &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func (c *Client) fetchItem(ctx context.Context, id int64) (apiItem, error) {
	var item *apiItem
	description := fmt.Sprintf("Hacker News item %d", id)
	if err := c.getJSON(ctx, fmt.Sprintf("%s/item/%d.json", strings.TrimRight(c.apiBase, "/"), id), description, &item); err != nil {
		return apiItem{}, err
	}
	if item == nil {
		return apiItem{}, fmt.Errorf("Hacker News item %d was not found", id)
	}
	return *item, nil
}

func (c *Client) getJSON(ctx context.Context, target, description string, result any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Golems/1 hacker-news")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", description, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return fmt.Errorf("%s returned HTTP %d", description, response.StatusCode)
	}
	if err := decodeBoundedJSON(response.Body, result); err != nil {
		return fmt.Errorf("decode %s: %w", description, err)
	}
	return nil
}

func (c *Client) fetchItems(ctx context.Context, ids []int64) []itemResult {
	results := make([]itemResult, len(ids))
	semaphore := make(chan struct{}, maxConcurrentRequests)
	var wait sync.WaitGroup
	for index, id := range ids {
		wait.Add(1)
		go func(index int, id int64) {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			results[index].item, results[index].err = c.fetchItem(ctx, id)
		}(index, id)
	}
	wait.Wait()
	return results
}

type commentTask struct {
	id     int64
	parent *Comment
}

func (c *Client) collectComments(ctx context.Context, ids []int64) ([]*Comment, string) {
	rootCount := min(len(ids), maxTopLevelComments)
	current := make([]commentTask, 0, rootCount)
	for _, id := range ids[:rootCount] {
		current = append(current, commentTask{id: id})
	}
	scheduled := len(current)
	roots := make([]*Comment, 0, rootCount)
	failed := 0
	var firstErr error

	for depth := 0; depth < maxCommentDepth && len(current) > 0; depth++ {
		ids := make([]int64, len(current))
		for index, task := range current {
			ids[index] = task.id
		}
		results := c.fetchItems(ctx, ids)
		next := make([]commentTask, 0)
		for index, result := range results {
			if result.err != nil {
				failed++
				if firstErr == nil {
					firstErr = result.err
				}
				continue
			}
			item := result.item
			if item.Deleted || item.Dead || item.Type != "comment" {
				continue
			}
			text := truncateRunes(cleanHTML(item.Text), maxCommentCharacters)
			if text == "" {
				continue
			}
			comment := &Comment{ID: item.ID, By: cleanOneLine(item.By), Text: text}
			if current[index].parent == nil {
				roots = append(roots, comment)
			} else {
				current[index].parent.Children = append(current[index].parent.Children, comment)
			}
			if depth+1 >= maxCommentDepth {
				continue
			}
			for _, childID := range item.Kids {
				if scheduled >= maxComments {
					break
				}
				next = append(next, commentTask{id: childID, parent: comment})
				scheduled++
			}
		}
		current = next
	}
	if failed > 0 {
		return roots, fmt.Sprintf("%d comment request(s) failed; first error: %v", failed, firstErr)
	}
	return roots, ""
}

func storyFromItem(item apiItem) Story {
	return Story{
		ID:          item.ID,
		Type:        cleanOneLine(item.Type),
		By:          cleanOneLine(item.By),
		PublishedAt: itemTime(item.Time),
		Title:       cleanOneLine(cleanHTML(item.Title)),
		Text:        cleanHTML(item.Text),
		URL:         cleanArticleURL(item.URL),
		Score:       item.Score,
		Comments:    item.Descendants,
	}
}

func storyFromSearchHit(hit searchHit) (Story, bool) {
	id, err := strconv.ParseInt(hit.ObjectID, 10, 64)
	if err != nil || id <= 0 {
		return Story{}, false
	}
	return Story{
		ID:          id,
		Type:        "story",
		By:          cleanOneLine(hit.Author),
		PublishedAt: itemTime(hit.CreatedAt),
		Title:       cleanOneLine(cleanHTML(hit.Title)),
		Text:        cleanHTML(hit.StoryText),
		URL:         cleanArticleURL(hit.URL),
		Score:       hit.Points,
		Comments:    hit.NumComments,
	}, true
}

func itemTime(unixSeconds int64) time.Time {
	if unixSeconds <= 0 {
		return time.Time{}
	}
	return time.Unix(unixSeconds, 0).UTC()
}

func decodeBoundedJSON(reader io.Reader, target any) error {
	limited := io.LimitReader(reader, maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return errors.New("response exceeds size limit")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}
