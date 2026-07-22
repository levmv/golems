package llm

import (
	"errors"
	"testing"

	"github.com/levmv/golems/pkg/openai"
)

func TestIsContextLengthError(t *testing.T) {
	for _, err := range []error{
		&Error{Code: "context_length_exceeded"},
		&Error{Message: "maximum context length is 128k"},
		&Error{Message: "too many tokens in request"},
	} {
		if !IsContextLengthError(err) {
			t.Fatalf("IsContextLengthError(%v) = false", err)
		}
	}
	if IsContextLengthError(errors.New("maximum context length")) {
		t.Fatal("plain error was classified as a provider context error")
	}
}

func TestWrapOpenAIErrorAPIError(t *testing.T) {
	apiErr := &openai.APIError{
		Code:           "invalid_api_key",
		Message:        "bad key",
		HTTPStatusCode: 401,
	}

	err := wrapOpenAIError(apiErr)

	var llmErr *Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("err = %T, want *Error", err)
	}
	if llmErr.StatusCode != 401 || llmErr.Code != "invalid_api_key" || llmErr.Provider != "openai" {
		t.Fatalf("llm error = %#v", llmErr)
	}
	var original *openai.APIError
	if !errors.As(err, &original) {
		t.Fatal("wrapped error does not expose original APIError")
	}
}

func TestWrapOpenAIErrorRequestError(t *testing.T) {
	reqErr := &openai.RequestError{
		HTTPStatusCode: 502,
		HTTPStatus:     "502 Bad Gateway",
		Body:           []byte("bad gateway"),
	}

	err := wrapOpenAIError(reqErr)

	var llmErr *Error
	if !errors.As(err, &llmErr) {
		t.Fatalf("err = %T, want *Error", err)
	}
	if llmErr.StatusCode != 502 || llmErr.Provider != "openai" {
		t.Fatalf("llm error = %#v", llmErr)
	}
	if llmErr.Message != "bad gateway" {
		t.Fatalf("message = %q, want body only", llmErr.Message)
	}
	var original *openai.RequestError
	if !errors.As(err, &original) {
		t.Fatal("wrapped error does not expose original RequestError")
	}
}
