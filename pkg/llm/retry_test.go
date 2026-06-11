package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

type failingClient struct {
	err         error
	chatCalls   int
	streamCalls int
}

func (c *failingClient) Chat(context.Context, *Request) (*Response, error) {
	c.chatCalls++
	return nil, c.err
}

func (c *failingClient) Stream(context.Context, *Request) (Stream, error) {
	c.streamCalls++
	return nil, c.err
}

func TestRetryClientStreamReturnsLastErrorWhenExhausted(t *testing.T) {
	wantErr := errors.New("temporary failure")
	client := &failingClient{err: wantErr}
	retry := &retryClient{client: client, maxRetries: 2}

	stream, err := retry.Stream(context.Background(), &Request{})
	if stream != nil {
		t.Fatalf("stream = %#v, want nil", stream)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if client.streamCalls != 3 {
		t.Fatalf("stream calls = %d, want 3", client.streamCalls)
	}
}

func TestRetryClientDoesNotRetryNonRetryableStatus(t *testing.T) {
	client := &failingClient{err: &Error{StatusCode: 400, Message: "bad request", Provider: "test"}}
	retry := &retryClient{client: client, maxRetries: 3}

	_, err := retry.Chat(context.Background(), &Request{})
	if err == nil {
		t.Fatal("Chat() err = nil, want error")
	}
	if client.chatCalls != 1 {
		t.Fatalf("chat calls = %d, want 1", client.chatCalls)
	}
}

func TestRetryClientDoesNotRetryInvalidRequest(t *testing.T) {
	client := &failingClient{err: fmt.Errorf("%w: tool choice function requires a name", ErrInvalidRequest)}
	retry := &retryClient{client: client, maxRetries: 3}

	_, err := retry.Chat(context.Background(), &Request{})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Chat() err = %v, want ErrInvalidRequest", err)
	}
	if client.chatCalls != 1 {
		t.Fatalf("chat calls = %d, want 1", client.chatCalls)
	}
}

func TestRetryClientRetriesRetryableStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "transport", err: &Error{Message: "connection reset", Provider: "test"}},
		{name: "timeout", err: &Error{StatusCode: 408, Message: "timeout", Provider: "test"}},
		{name: "rate limit", err: &Error{StatusCode: 429, Message: "rate limit", Provider: "test"}},
		{name: "server", err: &Error{StatusCode: 503, Message: "unavailable", Provider: "test"}},
		{name: "unknown", err: io.ErrUnexpectedEOF},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &failingClient{err: tt.err}
			retry := &retryClient{client: client, maxRetries: 2}

			_, err := retry.Chat(context.Background(), &Request{})
			if err == nil {
				t.Fatal("Chat() err = nil, want error")
			}
			if client.chatCalls != 3 {
				t.Fatalf("chat calls = %d, want 3", client.chatCalls)
			}
		})
	}
}
