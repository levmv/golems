package llm

import (
	"errors"
	"fmt"

	"github.com/levmv/golems/pkg/openai"
)

// ErrInvalidRequest marks request-construction errors that are deterministic
// and must not be retried.
var ErrInvalidRequest = errors.New("invalid request")

// Error is a provider-neutral LLM error.
type Error struct {
	StatusCode int
	Code       string
	Message    string
	Provider   string

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
	if err == nil {
		return nil
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return &Error{
			StatusCode: apiErr.HTTPStatusCode,
			Code:       errorCodeString(apiErr.Code),
			Message:    apiErr.Message,
			Provider:   "openai",
			err:        err,
		}
	}

	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		return &Error{
			StatusCode: reqErr.HTTPStatusCode,
			Message:    requestErrorMessage(reqErr),
			Provider:   "openai",
			err:        err,
		}
	}

	return &Error{
		Message:  err.Error(),
		Provider: "openai",
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
