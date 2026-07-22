package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type policyError struct {
	message string
}

func (e *policyError) Error() string { return e.message }

func newPolicyError(message string) error { return &policyError{message: message} }

func isPolicyError(err error) bool {
	var target *policyError
	return errors.As(err, &target)
}

type HTTPBackend struct {
	client *http.Client
}

func NewHTTPBackend() *HTTPBackend {
	return &HTTPBackend{client: safeHTTPClient()}
}

func (*HTTPBackend) Name() string { return "http" }

func (b *HTTPBackend) Fetch(ctx context.Context, request Request) (Result, error) {
	target, err := validatePublicURL(request.URL)
	if err != nil {
		return Result{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Result{}, err
	}
	httpRequest.Header.Set("Accept", "text/html, text/plain, application/json;q=0.8")
	httpRequest.Header.Set("User-Agent", "Golems/1 web-fetch")
	response, err := b.client.Do(httpRequest)
	if err != nil {
		return Result{}, fmt.Errorf("request URL: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
		return Result{}, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read page: %w", err)
	}
	truncated := len(raw) > maxResponseBytes
	if truncated {
		raw = raw[:maxResponseBytes]
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	text, title, err := extractContent(raw, contentType)
	if err != nil {
		return Result{}, err
	}
	finalURL := target.String()
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return Result{
		URL:       finalURL,
		Title:     title,
		Text:      text,
		Truncated: truncated,
	}, nil
}

func safeHTTPClient() *http.Client {
	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	// A proxy resolves and connects on our behalf, bypassing safeDial's DNS
	// checks. Direct fetching keeps the SSRF boundary local and auditable.
	transport.Proxy = nil
	transport.DialContext = safeDial
	return &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			_, err := validatePublicURL(request.URL.String())
			return err
		},
	}
}

func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !isPublicIP(address) {
			return nil, newPolicyError(fmt.Sprintf("destination %s resolves to a non-public address", host))
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("destination %s has no address", host)
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
}

func validatePublicURL(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Hostname() == "" {
		return nil, newPolicyError("URL must be absolute")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, newPolicyError("URL scheme must be http or https")
	}
	if target.User != nil {
		return nil, newPolicyError("URL credentials are not allowed")
	}
	hostname := strings.ToLower(target.Hostname())
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") {
		return nil, newPolicyError("private, loopback, and link-local destinations are not allowed")
	}
	if address := net.ParseIP(hostname); address != nil && !isPublicIP(address) {
		return nil, newPolicyError("private, loopback, and link-local destinations are not allowed")
	}
	return target, nil
}

func isPublicIP(address net.IP) bool {
	return address != nil && !address.IsLoopback() && !address.IsPrivate() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsUnspecified() && !address.IsMulticast()
}

func extractContent(raw []byte, contentType string) (text, title string, err error) {
	switch {
	case strings.Contains(contentType, "text/html") || strings.Contains(strings.ToLower(string(raw[:min(len(raw), 256)])), "<html"):
		document, parseErr := html.Parse(strings.NewReader(string(raw)))
		if parseErr != nil {
			return "", "", fmt.Errorf("parse HTML: %w", parseErr)
		}
		var blocks []string
		var walk func(*html.Node, bool)
		walk = func(node *html.Node, skipped bool) {
			if node.Type == html.ElementNode {
				switch node.Data {
				case "script", "style", "noscript", "svg", "canvas", "nav", "footer":
					skipped = true
				case "title":
					title = compactText(nodeText(node), 500)
				case "p", "li", "pre", "blockquote", "h1", "h2", "h3", "h4", "td", "th":
					if !skipped {
						if value := compactText(nodeText(node), 8000); value != "" {
							blocks = append(blocks, value)
						}
						return
					}
				}
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child, skipped)
			}
		}
		walk(document, false)
		return strings.Join(blocks, "\n\n"), title, nil
	case strings.Contains(contentType, "text/") || strings.Contains(contentType, "json") || contentType == "":
		return strings.TrimSpace(string(raw)), "", nil
	default:
		return "", "", fmt.Errorf("unsupported content type %q", contentType)
	}
}

func nodeText(node *html.Node) string {
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
			out.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return out.String()
}
