package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/levmv/golems/cy/internal/session"
)

func sessionDisplayTitle(summary session.Summary) string {
	if title := strings.TrimSpace(summary.Title); title != "" {
		return title
	}
	return "untitled session " + shortSessionID(summary.ID)
}

func relativeSessionTime(now, updated time.Time) string {
	if updated.IsZero() {
		return "unknown time"
	}
	elapsed := now.Sub(updated)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed/time.Hour))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(elapsed/(24*time.Hour)))
	case updated.Local().Year() == now.Local().Year():
		return updated.Local().Format("Jan 2")
	default:
		return updated.Local().Format("Jan 2, 2006")
	}
}
