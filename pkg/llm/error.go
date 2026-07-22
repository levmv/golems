package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/pkg/openai"
)

// ErrInvalidRequest marks request-construction errors that are deterministic
// and must not be retried.
var ErrInvalidRequest = errors.New("invalid request")

// IsContextLengthError reports whether a provider rejected a request because
// its model context was too large. Providers use several codes and messages for
// this condition, so the normalization belongs beside provider error mapping.
func IsContextLengthError(err error) bool {
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		return false
	}
	text := strings.ToLower(providerErr.Code + " " + providerErr.Message)
	return strings.Contains(text, "context_length") ||
		strings.Contains(text, "context length") ||
		strings.Contains(text, "maximum context") ||
		strings.Contains(text, "too many tokens")
}

// Error is a provider-neutral LLM error.
type Error struct {
	StatusCode int
	Code       string
	Message    string
	Provider   string
	RetryAfter time.Duration

	err error
}

func (e *Error) Error() string {
	if e.Message != "" {
		if e.StatusCode > 0 {
			return fmt.Sprintf("%s error, status code: %d, message: %s", e.Provider, e.StatusCode, e.Message)
		}
		return fmt.Sprintf("%s error: %s", e.Provider, e.Message)
	}
	if e.err != nil {
		return e.err.Error()
	}
	return "llm error"
}

func (e *Error) Unwrap() error {
	return e.err
}

func wrapOpenAIError(err error) error {
	return wrapProviderError("openai", err)
}

func wrapProviderError(provider string, err error) error {
	if err == nil {
		return nil
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return &Error{
			StatusCode: apiErr.HTTPStatusCode,
			Code:       errorCodeString(apiErr.Code),
			Message:    apiErr.Message,
			Provider:   provider,
			RetryAfter: apiErr.RetryAfter,
			err:        err,
		}
	}

	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return &Error{
			StatusCode: reqErr.HTTPStatusCode,
			Message:    requestErrorMessage(reqErr),
			Provider:   provider,
			RetryAfter: reqErr.RetryAfter,
			err:        err,
		}
	}

	return &Error{
		Message:  err.Error(),
		Provider: provider,
		err:      err,
	}
}

func errorCodeString(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func requestErrorMessage(err *openai.RequestError) string {
	if err.Err != nil {
		return err.Err.Error()
	}
	return string(err.Body)
}
