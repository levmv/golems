package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/brevity/internal/source"
)

const (
	jinaReaderBaseURL = "https://r.jina.ai/"
	jinaHTTPTimeout   = 45 * time.Second
)

type Jina struct {
	client *http.Client
}

func NewJina() *Jina {
	return &Jina{client: &http.Client{Timeout: jinaHTTPTimeout}}
}

func (f *Jina) Fetch(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := validateHTTPURL(rawURL)
	if err != nil {
		return source.Document{}, err
	}

	readerURL := jinaReaderBaseURL + parsed.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readerURL, nil)
	if err != nil {
		return source.Document{}, err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "BrevityBot/0.1 (+https://github.com/levmv/golems)")
	req.Header.Set("X-Respond-With", "frontmatter")

	resp, err := f.client.Do(req)
	if err != nil {
		return source.Document{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return source.Document{}, err
	}
	if int64(len(body)) > maxBodyBytes {
		return source.Document{}, fmt.Errorf("Jina Reader response is larger than %d bytes", maxBodyBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return source.Document{}, fmt.Errorf("Jina Reader returned HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(body) {
		body = bytes.ToValidUTF8(body, []byte(" "))
	}

	title, finalURL, text := parseJinaFrontmatter(string(body))
	text = trimRunes(strings.TrimSpace(text), maxSourceChars)
	if text == "" {
		return source.Document{}, fmt.Errorf("Jina Reader returned empty text")
	}
	if finalURL == "" {
		finalURL = parsed.String()
	}

	return source.Document{
		URL:         parsed.String(),
		FinalURL:    finalURL,
		Title:       title,
		Text:        text,
		ContentType: "text/markdown; source=jina-reader",
		FetchedAt:   time.Now(),
	}, nil
}

func parseJinaFrontmatter(raw string) (title, finalURL, text string) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "---\n") {
		return parseJinaPlainHeader(raw)
	}

	rest := strings.TrimPrefix(raw, "---\n")
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return parseJinaPlainHeader(raw)
	}

	frontmatter := rest[:end]
	text = strings.TrimSpace(rest[end+len("\n---"):])
	for _, line := range strings.Split(frontmatter, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = cleanFrontmatterValue(value)
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "title":
			title = value
		case "url", "sourceurl", "url source":
			finalURL = value
		}
	}
	return title, finalURL, text
}

func parseJinaPlainHeader(raw string) (title, finalURL, text string) {
	lines := strings.Split(raw, "\n")
	bodyStart := 0

header:
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyStart = i + 1
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			break
		}
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "title":
			title = strings.TrimSpace(value)
		case "url source", "url", "sourceurl":
			finalURL = strings.TrimSpace(value)
		default:
			break header
		}
		bodyStart = i + 1
	}
	return title, finalURL, strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
}

func cleanFrontmatterValue(value string) string {
	value = strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return strings.TrimSpace(unquoted)
	}
	return strings.Trim(value, `"'`)
}
