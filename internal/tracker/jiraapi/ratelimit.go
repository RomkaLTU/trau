package jiraapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitError is a 429 the retry ladder could not outlast, carrying when the
// site said the budget refills — zero when the response named no time — so a
// caller waits exactly that long rather than guessing.
type RateLimitError struct {
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	if e.ResetAt.IsZero() {
		return ErrRateLimited.Error()
	}
	return ErrRateLimited.Error() + " (budget resets " + e.ResetAt.UTC().Format(time.RFC3339) + ")"
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// retryAfterReset reads when a Retry-After header says the budget refills, in
// either form the header takes: delta-seconds from now, or an HTTP-date. An
// absent or unreadable header leaves the zero time, so the caller backs off on
// its own cadence instead of on a made-up one.
func retryAfterReset(header string, now time.Time) time.Time {
	header = strings.TrimSpace(header)
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return now.Add(time.Duration(secs) * time.Second)
	}
	if at, err := http.ParseTime(header); err == nil {
		return at
	}
	return time.Time{}
}
