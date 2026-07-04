package analysis

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/levmv/golems/hugin/internal/models"
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

func TestExtractJSONObjectToleratesFencesAndProse(t *testing.T) {
	got := extractJSONObject("Here is the result:\n```json\n{\"severity\":\"normal\",\"summary\":\"ok\"}\n```\nThanks")
	want := `{"severity":"normal","summary":"ok"}`
	if got != want {
		t.Fatalf("extractJSONObject() = %q, want %q", got, want)
	}
}

func TestBuildPromptSortsMapBackedSections(t *testing.T) {
	prompt := buildPrompt(Input{
		CheckID: "disk",
		Current: &models.CollectorOutput{
			Check:  "disk",
			Status: models.StatusOK,
			Metrics: map[string]any{
				"z_metric": 1.0,
				"a_metric": 2.0,
			},
		},
		History: []storage.RunRecord{
			{
				Status:    "warning",
				Window:    "15m",
				CreatedAt: time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC),
				Metrics: map[string]any{
					"z_metric": 3.0,
					"a_metric": 4.0,
				},
			},
			{
				Status:    "ok",
				CreatedAt: time.Date(2026, 7, 4, 12, 15, 0, 0, time.UTC),
				Metrics: map[string]any{
					"z_metric": 5.0,
					"a_metric": 6.0,
				},
			},
		},
	})

	assertBefore(t, prompt, "  a_metric: 2", "  z_metric: 1")
	assertBefore(t, prompt, "  a_metric: 4.00", "  z_metric: 3.00")
	assertBefore(t, prompt, "  ok: 1", "  warning: 1")
	for _, want := range []string{
		"Recent runs (newest first):",
		"2026-07-04T12:30:00Z status=warning window=15m metrics={a_metric=4, z_metric=3}",
		"2026-07-04T12:15:00Z status=ok metrics={a_metric=6, z_metric=5}",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
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

func assertBefore(t *testing.T, text, first, second string) {
	t.Helper()
	firstIdx := strings.Index(text, first)
	secondIdx := strings.Index(text, second)
	if firstIdx < 0 || secondIdx < 0 {
		t.Fatalf("prompt missing %q or %q:\n%s", first, second, text)
	}
	if firstIdx > secondIdx {
		t.Fatalf("%q appears after %q:\n%s", first, second, text)
	}
}
