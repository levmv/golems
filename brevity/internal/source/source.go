package source

import "time"

type Document struct {
	URL         string
	FinalURL    string
	Title       string
	Text        string
	ContentType string
	FetchedAt   time.Time
}
