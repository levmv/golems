package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// APIError provides error information returned by the API.
type APIError struct {
	Code           any           `json:"code,omitempty"`
	Message        string        `json:"message"`
	Param          *string       `json:"param,omitempty"`
	Type           string        `json:"type,omitempty"`
	HTTPStatus     string        `json:"-"`
	HTTPStatusCode int           `json:"-"`
	RetryAfter     time.Duration `json:"-"`
}

// RequestError provides information about generic request errors.
type RequestError struct {
	HTTPStatus     string
	HTTPStatusCode int
	Err            error
	Body           []byte
	RetryAfter     time.Duration
}

type ErrorResponse struct {
	Error *APIError `json:"error,omitempty"`
}

func (e *APIError) Error() string {
	if e.HTTPStatusCode > 0 {
		return fmt.Sprintf("error, status code: %d, status: %s, message: %s", e.HTTPStatusCode, e.HTTPStatus, e.Message)
	}
	return e.Message
}

func (e *APIError) UnmarshalJSON(data []byte) error {
	// Use an alias to unmarshal standard fields without triggering an infinite recursive loop
	type Alias APIError
	var aux struct {
		*Alias
		Message json.RawMessage `json:"message"`
	}

	aux.Alias = (*Alias)(e)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// 1. Handle OpenAI's Tool Calling quirk where Message is an array of strings
	var msgString string
	if err := json.Unmarshal(aux.Message, &msgString); err == nil {
		e.Message = msgString
	} else {
		var msgArray []string
		if err = json.Unmarshal(aux.Message, &msgArray); err == nil {
			e.Message = strings.Join(msgArray, ", ")
		}
	}

	// 2. Fix the float64 issue for error codes (if OpenAI returns a number instead of a string)
	if floatCode, isFloat := e.Code.(float64); isFloat {
		e.Code = int(floatCode)
	}

	return nil
}

func (e *RequestError) Error() string {
	msg := fmt.Sprintf("error, status code: %d, status: %s", e.HTTPStatusCode, e.HTTPStatus)

	trimmed := bytes.TrimSpace(e.Body)
	if len(trimmed) > 0 {
		msg += fmt.Sprintf(", body: %s", string(trimmed))
	}

	if e.Err != nil {
		msg += fmt.Sprintf(", err: %v", e.Err)
	}

	return msg
}

func (e *RequestError) Unwrap() error {
	return e.Err
}
