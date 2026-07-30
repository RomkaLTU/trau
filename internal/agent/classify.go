package agent

import (
	"errors"
	"regexp"
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

// rateLimitPhrases are the markers a provider emits at a usage wall that stay
// unambiguous under the widest input any caller passes. The hub classifies a whole
// turn of raw provider JSON, so a marker loose enough to appear in ordinary
// metadata would stall healthy sessions.
var rateLimitPhrases = []string{
	"rate limit", // also covers "rate limited" and "rate limit reached"
	"rate_limit",
	"ratelimit",
	"usage limit",
	"usage_limit",
	"too many requests",
	"insufficient_quota",
	"quota_exceeded",
	"out of quota",
}

// rateLimitQuota matches "quota" only alongside a word that makes it a wall, in
// either order, so both "quota exceeded" and OpenAI's "you exceeded your current
// quota" match while a bare mention of a quota does not. The span is bounded and
// stops at a sentence break so two unrelated sentences cannot combine into a hit.
var rateLimitQuota = regexp.MustCompile(
	`quota[^.\n]{0,32}\b(?:exceeded|exhausted|reached|depleted)\b` +
		`|\b(?:exceeded|exhausted|reached|depleted|insufficient)\b[^.\n]{0,32}quota`)

// rateLimitStatus matches a 429 only where it reads as an HTTP status. This
// replaced a bare substring test for "429", which also matched those digits inside
// a session UUID, token count, duration or dollar cost — enough to flag healthy
// grill turns as rate-limited from their JSON envelope alone, which the hub then
// reported as "the grilling agent hit a provider usage or rate limit".
// Only separators may sit between the keyword and the code, so an "is_error" field
// cannot reach across intervening JSON values to an unrelated 429.
var rateLimitStatus = regexp.MustCompile(
	`\b(?:http|https|status|statuscode|status_code|code|error|err)\b\W{0,6}\b429\b` +
		`|\b429\b\D{0,24}\b(?:too many requests|rate[ _-]?limit)\b`)

// RateLimitedText is the text counterpart of [IsRateLimited] for callers that only
// have a child's combined output, not a typed error — the hub's grilling runner
// classifies a turn's stdout/stderr this way. It strips ANSI first, so a marker
// drawn with interleaved styling still matches.
func RateLimitedText(text string) bool {
	flat := strings.ToLower(StripANSI(text))
	for _, phrase := range rateLimitPhrases {
		if strings.Contains(flat, phrase) {
			return true
		}
	}
	return rateLimitQuota.MatchString(flat) || rateLimitStatus.MatchString(flat)
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
