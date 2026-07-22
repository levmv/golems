package webfetch

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
)

func TestToolIsAvailableWithoutConfiguration(t *testing.T) {
	tool := NewTool()
	if tool.Definition.Function.Name != "web_fetch" || tool.Effect != golem.ToolEffectExternal {
		t.Fatalf("tool = %#v", tool)
	}
}

func TestExtractContentDropsScriptsAndKeepsReadableBlocks(t *testing.T) {
	raw := []byte(`<html><head><title>  Useful page </title><script>steal()</script></head><body><nav>noise</nav><h1>Heading</h1><p>Hello <b>world</b>.</p><pre>go test ./...</pre><footer>noise</footer></body></html>`)
	text, title, err := extractContent(raw, "text/html; charset=utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if title != "Useful page" || !strings.Contains(text, "Heading") || !strings.Contains(text, "Hello world .") || !strings.Contains(text, "go test ./...") || strings.Contains(text, "steal") || strings.Contains(text, "noise") {
		t.Fatalf("title=%q text=%q", title, text)
	}
}

func TestRejectsNonPublicAndCredentialedURLs(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/admin", "http://169.254.169.254/latest", "http://localhost/admin", "file:///etc/passwd", "https://user:pass@example.com/"} {
		if _, err := validatePublicURL(raw); err == nil {
			t.Errorf("validatePublicURL(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestHTTPBackendDisablesProxyAndRejectsPrivateRedirects(t *testing.T) {
	client := safeHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("fetch transport proxy = %#v", client.Transport)
	}
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/private", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("private redirect was accepted")
	}
	if _, err := safeDial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("private dial was accepted")
	}
}

func TestFetcherFallsBackFromErrorAndEmptyContent(t *testing.T) {
	first := &backendStub{name: "first", err: context.DeadlineExceeded}
	second := &backendStub{name: "second", result: Result{URL: "https://example.com"}}
	third := &backendStub{name: "third", result: Result{URL: "https://example.com", Text: "found"}}
	fetcher := New(first, second, third)
	result, err := fetcher.Fetch(context.Background(), Request{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "found" || result.Backend != "third" || first.calls != 1 || second.calls != 1 || third.calls != 1 {
		t.Fatalf("result=%#v calls=%d,%d,%d", result, first.calls, second.calls, third.calls)
	}
}

func TestFetcherDoesNotSendPolicyRejectedURLToFallback(t *testing.T) {
	first := &backendStub{name: "http", err: newPolicyError("destination resolves to a non-public address")}
	fallback := &backendStub{name: "external", result: Result{Text: "must not be called"}}
	_, err := New(first, fallback).Fetch(context.Background(), Request{URL: "https://example.com"})
	if err == nil || !isPolicyError(err) {
		t.Fatalf("Fetch() error = %v", err)
	}
	if first.calls != 1 || fallback.calls != 0 {
		t.Fatalf("calls = %d,%d", first.calls, fallback.calls)
	}
}

func TestFetcherSkipsNonMatchingBackend(t *testing.T) {
	conditional := &matchingBackendStub{backendStub: backendStub{name: "special", result: Result{Text: "wrong"}}}
	fallback := &backendStub{name: "fallback", result: Result{Text: "found"}}
	result, err := New(conditional, fallback).Fetch(context.Background(), Request{URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != "fallback" || conditional.calls != 0 || fallback.calls != 1 {
		t.Fatalf("result=%#v calls=%d,%d", result, conditional.calls, fallback.calls)
	}
}

type backendStub struct {
	name   string
	result Result
	err    error
	calls  int
}

type matchingBackendStub struct {
	backendStub
}

func (*matchingBackendStub) Match(Request) bool { return false }

func (b *backendStub) Name() string { return b.name }

func (b *backendStub) Fetch(context.Context, Request) (Result, error) {
	b.calls++
	return b.result, b.err
}
