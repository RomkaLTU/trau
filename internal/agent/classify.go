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
	return RateLimitedText(err.Error())
}

// RateLimitedText is the text counterpart of [IsRateLimited] for callers that only
// have a child's combined output, not a typed error — the hub's grilling runner
// classifies a turn's stdout/stderr this way.
func RateLimitedText(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "rate_limit") || strings.Contains(low, "rate limit") ||
		strings.Contains(low, "usage limit") || strings.Contains(low, "quota") || strings.Contains(text, "429")
}

// IsAuthRequired reports whether err is (or wraps) [ErrAuthRequired] — a provider
// auth/login wall that retrying cannot clear.
func IsAuthRequired(err error) bool {
	return errors.Is(err, ErrAuthRequired)
}

// AuthWallText is the text counterpart of [IsAuthRequired]: it reports a provider
// auth/login wall from a child's combined output. It strips ANSI first, so a
// marker drawn with interleaved styling still matches.
func AuthWallText(text string) bool {
	return hasAuthFailure(text)
}
