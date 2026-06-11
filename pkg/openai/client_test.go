package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDefaultHTTPClientUsesResponseHeaderTimeout(t *testing.T) {
	client := DefaultHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
}

func TestNewClientWithConfigFillsDefaultHTTPClient(t *testing.T) {
	client := NewClientWithConfig(ClientConfig{})
	transport, ok := client.config.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.config.HTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
}

func TestHandleErrorRespLimitsBody(t *testing.T) {
	body := strings.Repeat("x", maxErrorResponseBodyBytes+1024)
	client := NewClientWithConfig(ClientConfig{})

	err := client.handleErrorResp(&http.Response{
		Status:     "502 Bad Gateway",
		StatusCode: http.StatusBadGateway,
		Body:       ioNopCloser{strings.NewReader(body)},
	})

	reqErr, ok := err.(*RequestError)
	if !ok {
		t.Fatalf("error = %T, want *RequestError", err)
	}
	if len(reqErr.Body) != maxErrorResponseBodyBytes {
		t.Fatalf("body len = %d, want %d", len(reqErr.Body), maxErrorResponseBodyBytes)
	}
}

func TestToolChoiceOmitsEmptyFunction(t *testing.T) {
	data, err := json.Marshal(ToolChoice{Type: ToolTypeFunction})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(data) != `{"type":"function"}` {
		t.Fatalf("json = %s", data)
	}
}

type ioNopCloser struct {
	*strings.Reader
}

func (c ioNopCloser) Close() error {
	return nil
}
