package fetch

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

var errUnsafeURL = errors.New("unsafe URL")

func validateHTTPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty URL")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are supported")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("URL host is empty")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("URLs with userinfo are not supported")
	}
	if isBlockedHost(parsed.Hostname()) {
		return nil, fmt.Errorf("%w: private or local hosts are not fetched", errUnsafeURL)
	}
	return parsed, nil
}

func isBlockedHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast()
}
