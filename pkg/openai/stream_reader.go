package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// streamReader reads a stream of ChatCompletionStreamResponse from an *http.Response.
type streamReader struct {
	emptyMessagesLimit uint
	isFinished         bool

	reader         *bufio.Reader
	response       *http.Response
	errAccumulator bytes.Buffer
}

func (stream *streamReader) Recv() (ChatCompletionStreamResponse, error) {
	rawLine, err := stream.RecvRaw()
	if err != nil {
		return ChatCompletionStreamResponse{}, err
	}

	var response ChatCompletionStreamResponse
	err = json.Unmarshal(rawLine, &response)
	if err != nil {
		return ChatCompletionStreamResponse{}, err
	}

	return response, nil
}

func (stream *streamReader) RecvRaw() ([]byte, error) {
	if stream.isFinished {
		return nil, io.EOF
	}

	return stream.processLines()
}

func (stream *streamReader) processLines() ([]byte, error) {
	var emptyMessagesCount uint

	for {
		rawLine, readErr := stream.reader.ReadBytes('\n')
		if readErr != nil {
			// If the stream ended or broke, check if we accumulated any API errors.
			if respErr := stream.unmarshalError(); respErr != nil {
				return nil, fmt.Errorf("api error: %w", respErr)
			}
			return nil, readErr
		}

		// Strip newline characters
		line := bytes.TrimSuffix(rawLine, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))

		// Ignore empty lines
		if len(line) == 0 {
			emptyMessagesCount++
			if emptyMessagesCount > stream.emptyMessagesLimit {
				return nil, errors.New("stream has sent too many empty messages")
			}
			continue
		}

		// Ignore SSE comments (like keep-alive pings)
		if line[0] == ':' {
			continue
		}

		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			// No colon found. This breaks SSE spec. It's likely a raw proxy error dump.
			stream.errAccumulator.Write(line)
			continue
		}

		// Consume an optional single leading space per SSE spec
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		// We only care about "data" fields
		if string(field) == "data" {
			emptyMessagesCount = 0

			if string(value) == "[DONE]" {
				stream.isFinished = true
				return nil, io.EOF
			}

			// Intercept mid-stream errors (e.g., OpenRouter / OpenAI timeouts)
			if bytes.HasPrefix(value, []byte(`{"error":`)) {
				stream.errAccumulator.Write(value)
				// We don't immediately return because the error JSON might be spread
				// across multiple lines if the proxy formatted it weirdly.
				continue
			}

			// We found a valid JSON payload chunk!
			return value, nil
		}

		// For any other SSE fields ("event", "id", "retry"), we just ignore them for now.
	}
}

func (stream *streamReader) unmarshalError() error {
	errBytes := stream.errAccumulator.Bytes()
	if len(errBytes) == 0 {
		return nil
	}

	var errResp ErrorResponse
	if err := json.Unmarshal(errBytes, &errResp); err != nil {
		// If it's not JSON, it might be raw HTML (e.g. 502 Bad Gateway).
		// We wrap it in a generic APIError so it isn't lost.
		return &APIError{Message: string(errBytes)}
	}

	if errResp.Error == nil {
		return nil
	}

	return errResp.Error
}

func (stream *streamReader) Close() error {
	return stream.response.Body.Close()
}
