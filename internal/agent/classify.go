package agent

import (
	"errors"
	"strings"
)

// IsRateLimited reports whether err is (or wraps) a provider rate/usage-limit
// signal — the transient wall that must pause a run blamelessly rather than burn
// retries, since retrying only re-hits it. There is no typed sentinel across the
// provider CLIs, so detection is by the error text they emit.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	low := strings.ToLower(s)
	return strings.Contains(low, "rate_limit") || strings.Contains(low, "rate limit") ||
		strings.Contains(low, "usage limit") || strings.Contains(low, "quota") || strings.Contains(s, "429")
}

// IsAuthRequired reports whether err is (or wraps) [ErrAuthRequired] — a provider
// auth/login wall that retrying cannot clear.
func IsAuthRequired(err error) bool {
	return errors.Is(err, ErrAuthRequired)
}
