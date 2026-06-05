package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/brevity/internal/extract"
	"github.com/levmv/golems/brevity/internal/source"
)

const (
	defaultHTTPTimeout = 25 * time.Second
	maxBodyBytes       = 8 << 20
	maxSourceChars     = 70000
)

type Extractor interface {
	Extract(raw, contentType string) extract.Result
}

type HTTP struct {
	client    *http.Client
	extractor Extractor
}

func NewHTTP(extractor Extractor) *HTTP {
	if extractor == nil {
		extractor = extract.Regex{}
	}

	fetcher := &HTTP{extractor: extractor}
	fetcher.client = &http.Client{
		Timeout: defaultHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return fmt.Errorf("too many redirects")
			}
			if _, err := validateHTTPURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
	return fetcher
}

func (f *HTTP) Fetch(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := validateHTTPURL(rawURL)
	if err != nil {
		return source.Document{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return source.Document{}, err
	}
	req.Header.Set("User-Agent", "BrevityBot/0.1 (+https://github.com/levmv/golems)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.2")

	resp, err := f.client.Do(req)
	if err != nil {
		return source.Document{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return source.Document{}, fmt.Errorf("source returned HTTP %s", resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return source.Document{}, err
	}
	if int64(len(body)) > maxBodyBytes {
		return source.Document{}, fmt.Errorf("source body is larger than %d bytes", maxBodyBytes)
	}
	body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(body) {
		body = bytes.ToValidUTF8(body, []byte(" "))
	}

	contentType := resp.Header.Get("Content-Type")
	readable := f.extractor.Extract(string(body), contentType)
	text := trimRunes(readable.Text, maxSourceChars)
	if strings.TrimSpace(text) == "" {
		return source.Document{}, fmt.Errorf("no readable text found")
	}

	return source.Document{
		URL:         parsed.String(),
		FinalURL:    resp.Request.URL.String(),
		Title:       readable.Title,
		Text:        text,
		ContentType: contentType,
		FetchedAt:   time.Now(),
	}, nil
}

func trimRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n\n[Текст источника обрезан из-за размера.]"
}
