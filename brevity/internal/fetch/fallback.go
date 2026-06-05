package fetch

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/levmv/golems/brevity/internal/source"
)

type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (source.Document, error)
}

type Fallback struct {
	Primary   Fetcher
	Secondary Fetcher
}

func WithFallback(primary, secondary Fetcher) *Fallback {
	return &Fallback{Primary: primary, Secondary: secondary}
}

func (f *Fallback) Fetch(ctx context.Context, rawURL string) (source.Document, error) {
	doc, err := f.Primary.Fetch(ctx, rawURL)
	if err == nil && !LooksThin(doc) {
		return doc, nil
	}

	if f.Secondary == nil {
		return doc, err
	}

	fallbackDoc, fallbackErr := f.Secondary.Fetch(ctx, rawURL)
	if fallbackErr == nil {
		return fallbackDoc, nil
	}
	if _, ok := AsNeedsHuman(fallbackErr); ok {
		return source.Document{}, fallbackErr
	}
	if err != nil {
		return source.Document{}, err
	}
	return doc, nil
}

func LooksThin(doc source.Document) bool {
	text := strings.TrimSpace(doc.Text)
	if utf8.RuneCountInString(text) < 700 {
		return true
	}
	if strings.Count(text, "\n") < 2 && utf8.RuneCountInString(text) < 1800 {
		return true
	}
	return false
}
