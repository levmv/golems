package resolve

import (
	"context"
	"net/url"

	"github.com/levmv/golems/brevity/internal/source"
)

type Resolver interface {
	Resolve(ctx context.Context, rawURL string) (source.Document, error)
}

type SiteResolver interface {
	Resolver
	Match(u *url.URL) bool
}

type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (source.Document, error)
}

type Default struct {
	fetcher Fetcher
}

func NewDefault(fetcher Fetcher) *Default {
	return &Default{fetcher: fetcher}
}

func (r *Default) Resolve(ctx context.Context, rawURL string) (source.Document, error) {
	return r.fetcher.Fetch(ctx, rawURL)
}

type Chain struct {
	sites    []SiteResolver
	fallback Resolver
}

func NewChain(fallback Resolver, sites ...SiteResolver) *Chain {
	return &Chain{sites: sites, fallback: fallback}
}

func (c *Chain) Resolve(ctx context.Context, rawURL string) (source.Document, error) {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Scheme != "" && parsed.Hostname() != "" {
		for _, site := range c.sites {
			if site.Match(parsed) {
				return site.Resolve(ctx, rawURL)
			}
		}
	}
	return c.fallback.Resolve(ctx, rawURL)
}
