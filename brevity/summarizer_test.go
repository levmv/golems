package main

import "testing"

func TestDecodeMarkedSummary(t *testing.T) {
	summary, err := decodeSummary(`<<<TITLE>>>
Заголовок
<<<SHORT>>>
Коротко
<<<FULL>>>
Подробно
<<<END>>>`)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Title != "Заголовок" || summary.ShortSummary != "Коротко" || summary.FullSummary != "Подробно" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestDecodeMarkedSummaryWithoutEnd(t *testing.T) {
	summary, err := decodeSummary(`<<<TITLE>>>
Заголовок
<<<SHORT>>>
Коротко
<<<FULL>>>
Подробно`)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FullSummary != "Подробно" {
		t.Fatalf("unexpected full summary: %q", summary.FullSummary)
	}
}

func TestDecodeJSONSummaryStillWorks(t *testing.T) {
	summary, err := decodeSummary(`{"title":"T","short_summary":"S","full_summary":"F"}`)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Title != "T" || summary.ShortSummary != "S" || summary.FullSummary != "F" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestDecodeLabeledSummary(t *testing.T) {
	summary, err := decodeSummary("TITLE: T\nSHORT: S\nFULL: F")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Title != "T" || summary.ShortSummary != "S" || summary.FullSummary != "F" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}
