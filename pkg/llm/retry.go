package llm

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

const maxDelay = time.Hour * 6

type retryClient struct {
	client     Client
	maxRetries int
	baseDelay  time.Duration
}

// Chat implements the Client interface with exponential backoff.
func (r *retryClient) Chat(ctx context.Context, req *Request) (*Response, error) {
	var err error
	for i := 0; i <= r.maxRetries; i++ {
		var resp *Response
		resp, err = r.client.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !IsRetryable(err) {
			return nil, err
		}

		if i < r.maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay(err, r.baseDelay, i)):
			}
		}
	}
	return nil, err
}

// Stream implements the Client interface with exponential backoff.
func (r *retryClient) Stream(ctx context.Context, req *Request) (Stream, error) {
	var err error
	for i := 0; i <= r.maxRetries; i++ {
		var stream Stream
		stream, err = r.client.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}

		if !IsRetryable(err) {
			return nil, err
		}

		if i < r.maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay(err, r.baseDelay, i)):
			}
		}
	}
	return nil, err
}

func backoffDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}

	delay := min(time.Duration(float64(base)*math.Pow(1.5, float64(attempt))), maxDelay)
	if delay <= 0 {
		return 0
	}

	jitter := delay / 4
	if jitter > 0 {
		delay += rand.N(jitter)
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	return delay
}

func retryDelay(err error, base time.Duration, attempt int) time.Duration {
	delay := backoffDelay(base, attempt)
	var providerErr *Error
	if errors.As(err, &providerErr) && providerErr.RetryAfter > delay {
		delay = min(providerErr.RetryAfter, maxDelay)
	}
	return delay
}

// IsRetryable reports whether retrying a failed provider request can reasonably
// succeed without changing the request. Agent runtimes may add recovery for
// non-retryable request errors such as an oversized context.
func IsRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrInvalidRequest) {
		return false
	}

	var llmErr *Error
	if errors.As(err, &llmErr) {
		switch {
		case llmErr.StatusCode == 0:
			return true
		case llmErr.StatusCode == 408 || llmErr.StatusCode == 429:
			return true
		case llmErr.StatusCode >= 500:
			return true
		default:
			return false
		}
	}

	return true
}
