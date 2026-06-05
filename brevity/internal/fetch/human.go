package fetch

import (
	"errors"
	"fmt"
	"strings"
)

type NeedsHumanError struct {
	URL        string
	SessionID  string
	BrowserURL string
	Reason     string
}

func (e *NeedsHumanError) Error() string {
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "browser needs manual action"
	}
	if e.URL == "" {
		return reason
	}
	return fmt.Sprintf("%s: %s", reason, e.URL)
}

func AsNeedsHuman(err error) (*NeedsHumanError, bool) {
	var human *NeedsHumanError
	if errors.As(err, &human) {
		return human, true
	}
	return nil, false
}
