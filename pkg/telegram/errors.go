package telegram

import (
	"errors"
	"fmt"
)

var (
	ErrForbidden       = errors.New("forbidden")
	ErrBadRequest      = errors.New("bad request")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrTooManyRequests = errors.New("too many requests")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("conflict")
)

type TooManyRequestsError struct {
	Message    string
	RetryAfter int
}

func (e *TooManyRequestsError) Error() string {
	return fmt.Sprintf("%s: retry after %d seconds", e.Message, e.RetryAfter)
}

func (e *TooManyRequestsError) Unwrap() error {
	return ErrTooManyRequests
}

// MigrateError indicates a chat has been migrated to a supergroup.
type MigrateError struct {
	Message         string
	MigrateToChatID int
}

func (e *MigrateError) Error() string {
	return fmt.Sprintf("%s: migrated to %d", e.Message, e.MigrateToChatID)
}
