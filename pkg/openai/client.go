package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxErrorResponseBodyBytes = 1 << 20

// Client is an OpenAI API client.
type Client struct {
	config ClientConfig
}

func NewClient(authToken string) *Client {
	config := DefaultConfig(authToken)
	return NewClientWithConfig(config)
}

// NewClientWithConfig creates new OpenAI API client for specified config.
func NewClientWithConfig(config ClientConfig) *Client {
	if config.HTTPClient == nil {
		config.HTTPClient = DefaultHTTPClient()
	}
	if config.Header == nil {
		config.Header = make(http.Header)
	}
	return &Client{
		config: config,
	}
}

func (c *Client) newRequest(ctx context.Context, method, url string, requestBody any) (*http.Request, error) {
	var bodyReader io.Reader

	if requestBody != nil {
		// If it's already an io.Reader (like a bytes.Buffer for file uploads), use it directly.
		if reader, ok := requestBody.(io.Reader); ok {
			bodyReader = reader
		} else {
			// Otherwise, assume it's a struct
			reqBytes, err := json.Marshal(requestBody)
			if err != nil {
				return nil, err
			}
			bodyReader = bytes.NewReader(reqBytes)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	c.setCommonHeaders(req)
	return req, nil
}

func (c *Client) sendRequest(req *http.Request, v any) error {
	res, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	if isFailureStatusCode(res) {
		return c.handleErrorResp(res)
	}

	if v != nil {
		return json.NewDecoder(res.Body).Decode(v)
	}

	return nil
}

func (c *Client) sendRequestStream(req *http.Request) (*streamReader, error) {
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if isFailureStatusCode(resp) {
		err = c.handleErrorResp(resp)
		_ = resp.Body.Close()
		return nil, err
	}

	return &streamReader{
		emptyMessagesLimit: 300,
		reader:             bufio.NewReader(resp.Body),
		response:           resp,
	}, nil
}

func (c *Client) setCommonHeaders(req *http.Request) {
	if c.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.AuthToken)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("Accept", "application/json")

	for k, v := range c.config.Header {
		for _, val := range v {
			req.Header.Add(k, val)
		}
	}
}

func isFailureStatusCode(resp *http.Response) bool {
	return resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest
}

func (c *Client) handleErrorResp(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorResponseBodyBytes))
	if err != nil {
		return fmt.Errorf("error, reading response body: %w", err)
	}
	var errRes ErrorResponse
	err = json.Unmarshal(body, &errRes)
	// If it's not JSON, or it is JSON but missing the OpenAI "error" object:
	if err != nil || errRes.Error == nil {
		return &RequestError{
			HTTPStatus:     resp.Status,
			HTTPStatusCode: resp.StatusCode,
			Body:           body,
		}
	}

	errRes.Error.HTTPStatus = resp.Status
	errRes.Error.HTTPStatusCode = resp.StatusCode
	return errRes.Error
}

// fullURL returns full URL for request.
func (c *Client) fullURL(suffix string) string {
	baseURL := strings.TrimRight(c.config.BaseURL, "/")

	return baseURL + suffix
}
