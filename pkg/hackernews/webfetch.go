package hackernews

import (
	"context"

	"github.com/levmv/golems/pkg/webfetch"
)

type FetchBackend struct {
	client *Client
}

func NewFetchBackend(client *Client) *FetchBackend {
	if client == nil {
		client = NewClient()
	}
	return &FetchBackend{client: client}
}

func (*FetchBackend) Name() string { return "hacker_news" }

func (*FetchBackend) Match(request webfetch.Request) bool {
	_, err := ParseItemID(request.URL)
	return err == nil
}

func (b *FetchBackend) Fetch(ctx context.Context, request webfetch.Request) (webfetch.Result, error) {
	thread, err := b.client.Thread(ctx, request.URL)
	if err != nil {
		return webfetch.Result{}, err
	}
	return webfetch.Result{
		URL:   ItemURL(thread.Story.ID),
		Title: thread.Story.Title,
		Text:  formatThread(thread),
	}, nil
}
