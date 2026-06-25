package murm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/golems/pkg/golem"
	"github.com/levmv/golems/pkg/llm"
)

type fakeModel struct{ reply string }

func (fakeModel) Chat(context.Context, llm.Request) (*llm.Response, error) {
	return &llm.Response{}, nil
}

func (m fakeModel) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return &fakeStream{chunks: []llm.StreamChunk{
		{Text: m.reply, FinishReason: llm.FinishReasonStop},
	}, usage: llm.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}, nil
}

type fakeStream struct {
	chunks []llm.StreamChunk
	usage  llm.Usage
	idx    int
}

func (s *fakeStream) Recv() (llm.StreamChunk, error) {
	if s.idx >= len(s.chunks) {
		return llm.StreamChunk{}, io.EOF
	}
	c := s.chunks[s.idx]
	s.idx++
	return c, nil
}
func (s *fakeStream) Usage() llm.Usage { return s.usage }
func (s *fakeStream) Close() error     { return nil }

type resolverFunc func(ctx context.Context, chatID string) (*golem.Agent, error)

func (f resolverFunc) Resolve(ctx context.Context, chatID string) (*golem.Agent, error) {
	return f(ctx, chatID)
}

func TestStartRunStreamsSSE(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) (*golem.Agent, error) {
		return golem.New(golem.Config{Model: fakeModel{reply: "hello world"}})
	})
	srv := httptest.NewServer(NewHandler(resolver, nil).Mux())
	defer srv.Close()

	body := strings.NewReader(`{"input":"hi","clientMessageId":"msg_user_1","stream":true}`)
	resp, err := http.Post(srv.URL+"/api/chats/c1/runs", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	out, _ := io.ReadAll(resp.Body)
	s := string(out)

	for _, want := range []string{
		"event: message_start",
		"event: text_delta",
		"hello world",
		"event: usage",
		"event: finish",
		`"reason":"stop"`,
		"id: evt_1",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("SSE output missing %q:\n%s", want, s)
		}
	}
}

func TestStartRunValidation(t *testing.T) {
	resolver := resolverFunc(func(context.Context, string) (*golem.Agent, error) {
		t.Fatal("resolver should not be called on invalid input")
		return nil, nil
	})
	srv := httptest.NewServer(NewHandler(resolver, nil).Mux())
	defer srv.Close()

	cases := []string{
		`{"clientMessageId":"m1"}`, // missing input
		`{"input":"hi"}`,           // missing clientMessageId
		`not json`,                 // malformed
	}
	for _, body := range cases {
		resp, err := http.Post(srv.URL+"/api/chats/c1/runs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, resp.StatusCode)
		}
	}
}
