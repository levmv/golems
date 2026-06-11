package openai

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestStreamReaderReturnsAccumulatedErrorBeforeDone(t *testing.T) {
	stream := newTestStreamReader(`data: {"error":{"message":"rate limited","type":"rate_limit","code":"rate_limit"}}
data: [DONE]
`)

	_, err := stream.RecvRaw()
	if err == nil {
		t.Fatal("RecvRaw() error = nil, want accumulated API error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("RecvRaw() error = %T, want APIError", err)
	}
	if apiErr.Message != "rate limited" {
		t.Fatalf("message = %q, want rate limited", apiErr.Message)
	}
}

func TestStreamReaderHandlesCommentsCRLFAndDone(t *testing.T) {
	stream := newTestStreamReader(": keep-alive\r\n\r\ndata: {\"id\":\"chatcmpl_1\",\"choices\":[]}\r\n\r\ndata: [DONE]\r\n")

	raw, err := stream.RecvRaw()
	if err != nil {
		t.Fatalf("RecvRaw() error = %v", err)
	}
	if string(raw) != `{"id":"chatcmpl_1","choices":[]}` {
		t.Fatalf("raw = %q", raw)
	}

	_, err = stream.RecvRaw()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second RecvRaw() error = %v, want EOF", err)
	}
}

func TestStreamReaderWrapsRawProxyError(t *testing.T) {
	stream := newTestStreamReader("<html>bad gateway</html>\n")

	_, err := stream.RecvRaw()
	if err == nil {
		t.Fatal("RecvRaw() error = nil, want proxy error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("RecvRaw() error = %T, want APIError", err)
	}
	if apiErr.Message != "<html>bad gateway</html>" {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func newTestStreamReader(data string) *streamReader {
	return &streamReader{
		emptyMessagesLimit: 3,
		reader:             bufio.NewReader(strings.NewReader(data)),
	}
}
