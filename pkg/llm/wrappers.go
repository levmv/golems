package llm

import (
	"context"
	"errors"
	"io"
	"time"
)

// hookStream intercepts Stream events so we can track usage and errors when it finishes.
type hookStream struct {
	Stream

	model  string
	onDone func(model string, usage Usage, err error)
	done   bool
}

func (s *hookStream) Recv() (StreamChunk, error) {
	chunk, err := s.Stream.Recv()
	if err != nil && !s.done {
		s.done = true
		s.onDone(s.model, s.Usage(), err)
	}
	return chunk, err
}

func (s *hookStream) Close() error {
	err := s.Stream.Close()
	if !s.done {
		s.done = true
		s.onDone(s.model, s.Usage(), err)
	}
	return err
}

// --- Logging Decorator ---

type loggingClient struct {
	client Client
	logger Logger
}

func (l *loggingClient) Chat(ctx context.Context, req *Request) (*Response, error) {
	start := time.Now()
	l.logger.Debug("LLM chat started [model: %s]", req.Model)

	resp, err := l.client.Chat(ctx, req)
	duration := time.Since(start)

	if err != nil {
		l.logger.Error("LLM chat failed [model: %s, duration: %s, error: %v]", req.Model, duration, err)
	} else {
		l.logger.Info("LLM chat finished [model: %s, duration: %s, total_tokens: %d]", req.Model, duration, resp.Usage.TotalTokens)
	}

	return resp, err
}

func (l *loggingClient) Stream(ctx context.Context, req *Request) (Stream, error) {
	start := time.Now()
	l.logger.Debug("LLM stream started [model: %s]", req.Model)

	stream, err := l.client.Stream(ctx, req)
	if err != nil {
		l.logger.Error("LLM stream failed to start [model: %s, error: %v]", req.Model, err)
		return nil, err
	}

	return &hookStream{
		Stream: stream,
		model:  req.Model,
		onDone: func(model string, usage Usage, streamErr error) {
			duration := time.Since(start)
			if streamErr != nil && !errors.Is(streamErr, io.EOF) {
				l.logger.Error("LLM stream failed [model: %s, duration: %v, error: %v]", model, duration, streamErr)
			} else {
				l.logger.Info("LLM stream finished [model: %s, duration: %v, total_tokens: %d]", model, duration, usage.TotalTokens)
			}
		},
	}, nil
}

// --- Usage Tracking Decorator ---

type usageClient struct {
	client  Client
	tracker UsageTracker
}

func (u *usageClient) Chat(ctx context.Context, req *Request) (*Response, error) {
	resp, err := u.client.Chat(ctx, req)
	if err == nil {
		u.tracker.RecordUsage(req.Model, resp.Usage)
	}
	return resp, err
}

func (u *usageClient) Stream(ctx context.Context, req *Request) (Stream, error) {
	stream, err := u.client.Stream(ctx, req)
	if err != nil {
		return nil, err
	}

	return &hookStream{
		Stream: stream,
		model:  req.Model,
		onDone: func(model string, usage Usage, err error) {
			if err == nil || errors.Is(err, io.EOF) {
				u.tracker.RecordUsage(model, usage)
			}
		},
	}, nil
}
