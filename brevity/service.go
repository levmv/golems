package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/levmv/golems/brevity/internal/source"
)

type Summary struct {
	Title        string `json:"title"`
	ShortSummary string `json:"short_summary"`
	FullSummary  string `json:"full_summary"`
}

type PublishedPage struct {
	URL  string
	Path string
}

type Result struct {
	Source       source.Document
	Summary      Summary
	PublishedURL string
	PublishErr   error
}

type Resolver interface {
	Resolve(ctx context.Context, rawURL string) (source.Document, error)
}

type Summarizer interface {
	Summarize(ctx context.Context, source source.Document) (Summary, error)
}

type Publisher interface {
	Publish(ctx context.Context, source source.Document, summary Summary) (PublishedPage, error)
}

type Service struct {
	resolver   Resolver
	summarizer Summarizer
	publisher  Publisher
}

func NewService(resolver Resolver, summarizer Summarizer, publisher Publisher) *Service {
	return &Service{
		resolver:   resolver,
		summarizer: summarizer,
		publisher:  publisher,
	}
}

func (s *Service) SummarizeURL(ctx context.Context, rawURL string) (*Result, error) {
	source, err := s.resolver.Resolve(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}

	summary, err := s.summarizer.Summarize(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("summarize source: %w", err)
	}
	summary = normalizeSummary(summary, source)

	result := &Result{
		Source:  source,
		Summary: summary,
	}

	if s.publisher == nil {
		return result, nil
	}

	page, err := s.publisher.Publish(ctx, source, summary)
	if err != nil {
		result.PublishErr = fmt.Errorf("publish telegraph page: %w", err)
		return result, nil
	}
	result.PublishedURL = page.URL
	return result, nil
}

func normalizeSummary(summary Summary, source source.Document) Summary {
	summary.Title = strings.TrimSpace(summary.Title)
	if summary.Title == "" {
		summary.Title = strings.TrimSpace(source.Title)
	}
	if summary.Title == "" {
		summary.Title = "Summary"
	}

	summary.ShortSummary = trimPlainRunes(strings.TrimSpace(summary.ShortSummary), 1800)
	summary.FullSummary = strings.TrimSpace(summary.FullSummary)
	return summary
}

func trimPlainRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}
