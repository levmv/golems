package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("17", now); got != 17*time.Second {
		t.Fatalf("seconds Retry-After = %s", got)
	}
	if got := parseRetryAfter(now.Add(45*time.Second).Format(http.TimeFormat), now); got != 45*time.Second {
		t.Fatalf("date Retry-After = %s", got)
	}
}

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

func TestChatCompletionMessageIncludesEmptyStringContent(t *testing.T) {
	data, err := json.Marshal(ChatCompletionMessage{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID:   "call_1",
			Type: ToolTypeFunction,
			Function: FunctionCall{
				Name:      "shell",
				Arguments: `{"command":"pwd"}`,
			},
		}},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"content":""`) {
		t.Fatalf("json missing empty content field: %s", data)
	}
	if strings.Contains(string(data), "MultiContent") {
		t.Fatalf("json leaked internal MultiContent field: %s", data)
	}
}

type ioNopCloser struct {
	*strings.Reader
}

func (c ioNopCloser) Close() error {
	return nil
}
