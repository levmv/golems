package models

// CollectorOutput represents the strict JSON payload a collector script must return.
type CollectorOutput struct {
	Check   string         `json:"check"`
	Status  Status         `json:"status"`            // "ok", "error", or "partial"
	Metrics map[string]any `json:"metrics,omitempty"` // Numeric, string, or bool values only
	Errors  []ErrorDetail  `json:"errors,omitempty"`
	Window  string         `json:"window,omitempty"` // e.g., "15m", "1h"
}

// Status represents the health of the collection process itself.
type Status string

const (
	StatusOK      Status = "ok"
	StatusError   Status = "error"
	StatusPartial Status = "partial"
)

// ErrorDetail provides structured error information to prevent LLM hallucinations.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
