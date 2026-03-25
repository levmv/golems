package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Parameters  struct {
		RetryAfter      int `json:"retry_after,omitempty"`
		MigrateToChatID int `json:"migrate_to_chat_id,omitempty"`
	} `json:"parameters,omitempty"`
}

func (b *Bot) rawRequest(ctx context.Context, method string, params any, dest any) error {
	url := fmt.Sprintf("%s/bot%s/%s", b.url, b.token, method)

	needsMultipart := false
	if params != nil {
		needsMultipart = checkNeedsMultipart(params)
	}

	var req *http.Request
	var err error

	if !needsMultipart {
		// The fast path
		var body []byte
		if params != nil {
			body, err = json.Marshal(params)
			if err != nil {
				return fmt.Errorf("marshal json: %w", err)
			}
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

	} else {
		pr, pw := io.Pipe()
		writer := multipart.NewWriter(pw)

		go func() {
			err = buildMultipartSafe(writer, params)
			if closeErr := writer.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil {
				_ = pw.CloseWithError(fmt.Errorf("build form: %w", err))
			} else {
				_ = pw.Close()
			}
		}()

		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
		if err != nil {
			_ = pr.Close()
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
	}

	if b.isDebug && !strings.Contains(method, "getUpdates") {
		b.logger.Debug("request: %s", b.sanitize(url))
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %s", b.sanitize(err.Error()))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	r := apiResponse{}
	if err = json.Unmarshal(body, &r); err != nil {
		// If it's not JSON, and the status code is bad, it's likely a Gateway timeout/HTML error
		if resp.StatusCode >= 500 {
			return fmt.Errorf("telegram server error %d: %s", resp.StatusCode, string(body))
		}
		return fmt.Errorf("decode response: %s, %w", body, err)
	}

	if !r.OK {
		switch r.ErrorCode {
		case http.StatusForbidden:
			return fmt.Errorf("%w: %s", ErrForbidden, r.Description)
		case http.StatusBadRequest:
			if r.Parameters.MigrateToChatID != 0 {
				return &MigrateError{
					Message:         fmt.Sprintf("%s: %s", ErrBadRequest, r.Description),
					MigrateToChatID: r.Parameters.MigrateToChatID,
				}
			}
			return fmt.Errorf("%w: %s", ErrBadRequest, r.Description)
		case http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", ErrUnauthorized, r.Description)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrNotFound, r.Description)
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", ErrConflict, r.Description)
		case http.StatusTooManyRequests:
			return &TooManyRequestsError{
				Message:    fmt.Sprintf("%s: %s", ErrTooManyRequests, r.Description),
				RetryAfter: r.Parameters.RetryAfter,
			}
		default:
			return fmt.Errorf("telegram error %d: %s", r.ErrorCode, r.Description)
		}
	}

	if dest != nil && len(r.Result) > 0 {
		if err = json.Unmarshal(r.Result, dest); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}

	return nil
}

func (b *Bot) sanitize(s string) string {
	if b.token != "" {
		s = strings.ReplaceAll(s, b.token, "[***]")
	}
	if b.webhookSecretToken != "" {
		s = strings.ReplaceAll(s, b.webhookSecretToken, "[***]")
	}
	return s
}

func checkNeedsMultipart(params any) bool {
	v := reflect.ValueOf(params)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}

	for i := range v.NumField() {
		field := v.Field(i)
		if file, ok := field.Interface().(InputFile); ok {
			if file.NeedsUpload() {
				return true
			}
		}
	}
	return false
}

func buildMultipartSafe(writer *multipart.Writer, params any) error {
	v := reflect.ValueOf(params)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	t := v.Type()
	for i := range v.NumField() {
		field := t.Field(i)
		val := v.Field(i)

		// Get the JSON tag (e.g., `json:"chat_id"`)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}

		// Handle Files
		if file, ok := val.Interface().(InputFile); ok {
			if file.NeedsUpload() {
				part, err := writer.CreateFormFile(tag, file.Filename)
				if err != nil {
					return err
				}
				if _, err = io.Copy(part, file.Reader); err != nil {
					return err
				}

				// Important: If it's a file from disk, close it!
				if closer, ok := file.Reader.(io.Closer); ok {
					_ = closer.Close()
				}
			} else if file.StringValue != "" {
				// It's just a URL or ID, write as a normal text field
				if err := writer.WriteField(tag, file.StringValue); err != nil {
					return err
				}
			}
			continue
		}

		// Handle regular fields (strings, ints, bools)
		if (val.Kind() == reflect.Ptr || val.Kind() == reflect.Interface) && !val.IsNil() {
			val = val.Elem()
		}

		if val.IsZero() && strings.Contains(field.Tag.Get("json"), "omitempty") {
			continue // Skip empty optional fields
		}

		var strVal string
		switch val.Kind() {
		case reflect.String:
			strVal = val.String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			strVal = strconv.FormatInt(val.Int(), 10)
		case reflect.Bool:
			strVal = strconv.FormatBool(val.Bool())
		case reflect.Struct, reflect.Slice, reflect.Map:
			// Complex things like ReplyMarkup or Entities need to be JSON strings
			b, _ := json.Marshal(val.Interface())
			strVal = string(b)
		}

		if strVal != "" {
			if err := writer.WriteField(tag, strVal); err != nil {
				return err
			}
		}
	}
	return nil
}
