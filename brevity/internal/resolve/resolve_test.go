package resolve

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/levmv/golems/brevity/internal/source"
)

type fakeResolver struct {
	doc source.Document
	err error
}

func (r fakeResolver) Resolve(context.Context, string) (source.Document, error) {
	return r.doc, r.err
}

type fakeSite struct {
	fakeResolver
	matched bool
}

func (s fakeSite) Match(*url.URL) bool { return s.matched }

func TestChainUsesMatchingSite(t *testing.T) {
	chain := NewChain(
		fakeResolver{doc: source.Document{Title: "fallback"}},
		fakeSite{fakeResolver: fakeResolver{doc: source.Document{Title: "site"}}, matched: true},
	)

	doc, err := chain.Resolve(context.Background(), "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "site" {
		t.Fatalf("expected site resolver, got %q", doc.Title)
	}
}

func TestChainFallsBackForUnmatchedSites(t *testing.T) {
	chain := NewChain(
		fakeResolver{doc: source.Document{Title: "fallback"}},
		fakeSite{fakeResolver: fakeResolver{err: errors.New("should not be called")}, matched: false},
	)

	doc, err := chain.Resolve(context.Background(), "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "fallback" {
		t.Fatalf("expected fallback resolver, got %q", doc.Title)
	}
}
