package openai

import (
	"net/http"
	"strconv"
	"time"
)

type RateLimitHeaders struct {
	LimitRequests     int       `json:"x-ratelimit-limit-requests"`
	LimitTokens       int       `json:"x-ratelimit-limit-tokens"`
	RemainingRequests int       `json:"x-ratelimit-remaining-requests"`
	RemainingTokens   int       `json:"x-ratelimit-remaining-tokens"`
	ResetRequests     ResetTime `json:"x-ratelimit-reset-requests"`
	ResetTokens       ResetTime `json:"x-ratelimit-reset-tokens"`
}

type ResetTime string

func (r ResetTime) String() string {
	return string(r)
}

func (r ResetTime) Time() time.Time {
	d, _ := time.ParseDuration(string(r))
	return time.Now().Add(d)
}

func ParseRateLimitHeaders(h http.Header) RateLimitHeaders {
	limitReq, _ := strconv.Atoi(h.Get("X-Ratelimit-Limit-Requests"))
	limitTokens, _ := strconv.Atoi(h.Get("X-Ratelimit-Limit-Tokens"))
	remainingReq, _ := strconv.Atoi(h.Get("X-Ratelimit-Remaining-Requests"))
	remainingTokens, _ := strconv.Atoi(h.Get("X-Ratelimit-Remaining-Tokens"))
	return RateLimitHeaders{
		LimitRequests:     limitReq,
		LimitTokens:       limitTokens,
		RemainingRequests: remainingReq,
		RemainingTokens:   remainingTokens,
		ResetRequests:     ResetTime(h.Get("X-Ratelimit-Reset-Requests")),
		ResetTokens:       ResetTime(h.Get("X-Ratelimit-Reset-Tokens")),
	}
}
