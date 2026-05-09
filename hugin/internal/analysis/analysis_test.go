package analysis

import (
	"testing"
	"unicode/utf8"

	"github.com/levmv/golems/hugin/internal/storage"
)

func TestComputeHistorySummarySkipsNonNumericMetrics(t *testing.T) {
	summary := computeHistorySummary([]storage.RunRecord{
		{
			Metrics: map[string]any{
				"used_pct": 75.0,
				"status":   "ok",
				"enabled":  true,
			},
		},
		{
			Metrics: map[string]any{
				"used_pct": 85.0,
				"status":   "warning",
			},
		},
	})

	got, ok := summary["used_pct"]
	if !ok {
		t.Fatalf("expected numeric metric summary, got %#v", summary)
	}
	if got.Min != 75 || got.Avg != 80 || got.Max != 85 {
		t.Fatalf("unexpected numeric summary: %+v", got)
	}
	if _, ok := summary["status"]; ok {
		t.Fatalf("expected string metric to be skipped, got %#v", summary["status"])
	}
	if _, ok := summary["enabled"]; ok {
		t.Fatalf("expected bool metric to be skipped, got %#v", summary["enabled"])
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("абвгд", 5)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %q", got)
	}
	if got != "аб..." {
		t.Fatalf("expected truncate to cut at rune boundary, got %q", got)
	}
}
