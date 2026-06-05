package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/golems/brevity/internal/extract"
	"github.com/levmv/golems/brevity/internal/source"
)

const (
	hnAPIBase             = "https://hacker-news.firebaseio.com/v0"
	hnHTTPTimeout         = 20 * time.Second
	hnMaxTopLevelComments = 15
	hnMaxCommentDepth     = 3
	hnMaxComments         = 45
	hnMaxCommentChars     = 900
)

type HN struct {
	articleResolver Resolver
	client          *http.Client
	apiBase         string
}

func NewHN(articleResolver Resolver) *HN {
	return &HN{
		articleResolver: articleResolver,
		client:          &http.Client{Timeout: hnHTTPTimeout},
		apiBase:         hnAPIBase,
	}
}

func (r *HN) Match(u *url.URL) bool {
	_, ok := hnItemID(u)
	return ok
}

func (r *HN) Resolve(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return source.Document{}, err
	}
	id, ok := hnItemID(parsed)
	if !ok {
		return source.Document{}, fmt.Errorf("not a Hacker News item URL: %s", rawURL)
	}

	story, err := r.fetchItem(ctx, id)
	if err != nil {
		return source.Document{}, err
	}
	if story.Deleted || story.Dead {
		return source.Document{}, fmt.Errorf("Hacker News item %d is deleted or dead", id)
	}

	title := cleanHNTitle(story.Title)
	if title == "" {
		title = fmt.Sprintf("Hacker News item %d", id)
	}

	var article source.Document
	var articleErr error
	if story.URL != "" && r.articleResolver != nil {
		article, articleErr = r.articleResolver.Resolve(ctx, story.URL)
	}

	comments, commentsErr := r.collectComments(ctx, story.Kids)
	text := r.renderDocument(story, rawURL, article, articleErr, comments, commentsErr)

	return source.Document{
		URL:         rawURL,
		FinalURL:    rawURL,
		Title:       title,
		Text:        text,
		ContentType: "text/x-brevity-hn",
		FetchedAt:   time.Now(),
	}, nil
}

func hnItemID(u *url.URL) (int64, bool) {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "news.ycombinator.com" || strings.Trim(u.Path, "/") != "item" {
		return 0, false
	}
	id, err := strconv.ParseInt(u.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

type hnItem struct {
	ID          int64   `json:"id"`
	Deleted     bool    `json:"deleted"`
	Type        string  `json:"type"`
	By          string  `json:"by"`
	Time        int64   `json:"time"`
	Text        string  `json:"text"`
	Dead        bool    `json:"dead"`
	Parent      int64   `json:"parent"`
	Kids        []int64 `json:"kids"`
	URL         string  `json:"url"`
	Score       int     `json:"score"`
	Title       string  `json:"title"`
	Descendants int     `json:"descendants"`
}

type hnComment struct {
	ID       int64
	By       string
	Text     string
	Children []hnComment
}

func (r *HN) fetchItem(ctx context.Context, id int64) (hnItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/item/%d.json", strings.TrimRight(r.apiBase, "/"), id), nil)
	if err != nil {
		return hnItem{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BrevityBot/0.1")

	resp, err := r.client.Do(req)
	if err != nil {
		return hnItem{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return hnItem{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return hnItem{}, fmt.Errorf("HN API returned HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(string(body)) == "null" {
		return hnItem{}, fmt.Errorf("HN item %d not found", id)
	}

	var item hnItem
	if err = json.Unmarshal(body, &item); err != nil {
		return hnItem{}, err
	}
	return item, nil
}

func (r *HN) collectComments(ctx context.Context, ids []int64) ([]hnComment, error) {
	var comments []hnComment
	var count int
	var firstErr error

	for i, id := range ids {
		if i >= hnMaxTopLevelComments || count >= hnMaxComments {
			break
		}
		comment, ok, err := r.collectComment(ctx, id, 0, &count)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ok {
			comments = append(comments, comment)
		}
	}
	if len(comments) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return comments, firstErr
}

func (r *HN) collectComment(ctx context.Context, id int64, depth int, count *int) (hnComment, bool, error) {
	if *count >= hnMaxComments {
		return hnComment{}, false, nil
	}

	item, err := r.fetchItem(ctx, id)
	if err != nil {
		return hnComment{}, false, err
	}
	if item.Deleted || item.Dead || item.Type != "comment" {
		return hnComment{}, false, nil
	}

	text := cleanHNText(item.Text)
	if text == "" {
		return hnComment{}, false, nil
	}

	*count++
	comment := hnComment{
		ID:   item.ID,
		By:   item.By,
		Text: trimRunes(text, hnMaxCommentChars),
	}

	if depth+1 >= hnMaxCommentDepth {
		return comment, true, nil
	}
	for _, childID := range item.Kids {
		if *count >= hnMaxComments {
			break
		}
		child, ok, childErr := r.collectComment(ctx, childID, depth+1, count)
		if childErr != nil && err == nil {
			err = childErr
		}
		if ok {
			comment.Children = append(comment.Children, child)
		}
	}
	return comment, true, err
}

func (r *HN) renderDocument(story hnItem, hnURL string, article source.Document, articleErr error, comments []hnComment, commentsErr error) string {
	var sb strings.Builder
	title := cleanHNTitle(story.Title)

	sb.WriteString("# Hacker News item\n\n")
	writeLine(&sb, "Title", title)
	writeLine(&sb, "HN URL", hnURL)
	if story.URL != "" {
		writeLine(&sb, "Article URL", story.URL)
	}
	if story.By != "" {
		writeLine(&sb, "Posted by", story.By)
	}
	if story.Score > 0 {
		writeLine(&sb, "Score", strconv.Itoa(story.Score))
	}
	if story.Descendants > 0 {
		writeLine(&sb, "Total comments", strconv.Itoa(story.Descendants))
	}

	if story.URL != "" {
		sb.WriteString("\n# Article\n\n")
		if articleErr != nil {
			sb.WriteString("Article fetch failed: ")
			sb.WriteString(articleErr.Error())
			sb.WriteString("\n")
		} else {
			if article.Title != "" && article.Title != title {
				writeLine(&sb, "Article title", article.Title)
			}
			if article.FinalURL != "" && article.FinalURL != story.URL {
				writeLine(&sb, "Final URL", article.FinalURL)
			}
			sb.WriteString("\n")
			sb.WriteString(article.Text)
			sb.WriteString("\n")
		}
	} else if story.Text != "" {
		sb.WriteString("\n# Hacker News post text\n\n")
		sb.WriteString(cleanHNText(story.Text))
		sb.WriteString("\n")
	}

	sb.WriteString("\n# Hacker News discussion\n\n")
	sb.WriteString(fmt.Sprintf("Selection: HN ranked order, top %d roots, depth up to %d, max %d comments. HN API does not expose comment scores.\n\n", hnMaxTopLevelComments, hnMaxCommentDepth, hnMaxComments))
	if commentsErr != nil {
		sb.WriteString("Some comments failed to load: ")
		sb.WriteString(commentsErr.Error())
		sb.WriteString("\n\n")
	}
	if len(comments) == 0 {
		sb.WriteString("No comments loaded.\n")
	} else {
		for _, comment := range comments {
			renderComment(&sb, comment, 0)
		}
	}

	return strings.TrimSpace(sb.String())
}

func renderComment(sb *strings.Builder, comment hnComment, depth int) {
	indent := strings.Repeat("  ", depth)
	author := comment.By
	if author == "" {
		author = "unknown"
	}
	sb.WriteString(indent)
	sb.WriteString("- ")
	sb.WriteString(author)
	sb.WriteString(": ")
	sb.WriteString(oneLine(comment.Text))
	sb.WriteString("\n")
	for _, child := range comment.Children {
		renderComment(sb, child, depth+1)
	}
	if depth == 0 {
		sb.WriteString("\n")
	}
}

func writeLine(sb *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	sb.WriteString(key)
	sb.WriteString(": ")
	sb.WriteString(value)
	sb.WriteString("\n")
}

func cleanHNTitle(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

func cleanHNText(s string) string {
	return extract.ReadableText(s, "text/html").Text
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func trimRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}
