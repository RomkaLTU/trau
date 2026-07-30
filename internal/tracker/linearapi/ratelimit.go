package linearapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrRateLimited marks a request Linear refused because the API key's request
// budget for the current window is spent. It is transient — the budget refills
// when the window rolls — so a caller backs off to the reset time instead of
// treating the repo's tracker as broken.
var ErrRateLimited = errors.New("linear: rate limit exceeded")

// RateLimitError is a rate-limit refusal with what Linear said about it: its own
// message and, when the response carried the headers, the moment the window
// resets, so a caller waits exactly that long rather than guessing.
type RateLimitError struct {
	Message string
	ResetAt time.Time
}

func (e *RateLimitError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "rate limit exceeded"
	}
	if e.ResetAt.IsZero() {
		return "linear: " + msg
	}
	return "linear: " + msg + " (budget resets " + e.ResetAt.UTC().Format(time.RFC3339) + ")"
}

func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Linear meters every request against the API key rather than the caller
// (https://linear.app/developers/rate-limiting), so a hub syncing several repos
// spends one budget. The pacing below is shared by every client built for the same
// key, which is the only place the whole spend is visible.
const (
	// hourlyBudget is Linear's per-key allowance for a personal API key.
	hourlyBudget = 2500

	// budgetShare is how much of the budget the paced readers may spend, leaving
	// the rest for the bursty tracker writes a run makes on the same key.
	budgetShare = 0.8

	// steadyRate is the sustained request rate, in requests per second, the
	// limiter refills at when the API reports nothing more specific.
	steadyRate = hourlyBudget * budgetShare / 3600

	// minRate floors the observed rate so an almost-spent window still drains
	// slowly instead of dividing by zero.
	minRate = 1.0 / 60

	// burst is how many requests may go out back to back before pacing kicks in,
	// so a paging pull or a board opening is not slowed down.
	burst = 20

	// maxWait is the longest a request waits for a token. Past it the limiter
	// refuses, so the caller backs off on its own cadence rather than holding a
	// request open for the rest of the window.
	maxWait = 30 * time.Second
)

// limiter paces the requests spent against one API key: a token bucket refilled
// at the budget's sustained rate, clamped by whatever the API reports is left.
type limiter struct {
	mu      sync.Mutex
	tokens  float64
	rate    float64
	last    time.Time
	blocked time.Time
}

func newLimiter() *limiter {
	return &limiter{tokens: burst, rate: steadyRate, last: time.Now()}
}

// limiterKey scopes a bucket to what Linear actually meters: the API key, per
// endpoint, so a test server never shares a budget with the real API.
type limiterKey struct {
	apiKey   string
	endpoint string
}

// limiters holds one bucket per API key and endpoint, so every client in the
// process built for the same key paces against one budget.
var limiters sync.Map

func limiterFor(apiKey, target string) *limiter {
	key := limiterKey{apiKey: apiKey, endpoint: target}
	if l, ok := limiters.Load(key); ok {
		return l.(*limiter)
	}
	l, _ := limiters.LoadOrStore(key, newLimiter())
	return l.(*limiter)
}

// wait blocks until the key's budget allows another request, or returns a
// RateLimitError when that would take longer than maxWait.
func (l *limiter) wait(ctx context.Context) error {
	delay, err := l.reserve()
	if err != nil || delay <= 0 {
		return err
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// reserve takes a token and reports how long the caller must wait before spending
// it. Tokens are allowed to go negative so concurrent reservations queue behind
// each other instead of all waiting for the same refill.
func (l *limiter) reserve() (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > burst {
		l.tokens = burst
	}
	l.last = now

	wait := l.blocked.Sub(now)
	if l.tokens < 1 {
		if need := time.Duration((1 - l.tokens) / l.rate * float64(time.Second)); need > wait {
			wait = need
		}
	}
	if wait > maxWait {
		return 0, &RateLimitError{
			Message: "the API key's request budget is spent",
			ResetAt: l.blocked,
		}
	}
	l.tokens--
	if wait < 0 {
		return 0, nil
	}
	return wait, nil
}

// observe folds a response's rate-limit headers into the bucket. Linear's own
// count of what is left counts every request made against the key, including
// those from outside this process, so it caps the bucket, and the refill rate is
// paced to spread what is left over the rest of the window.
func (l *limiter) observe(remaining int, reset time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	share := float64(remaining) * budgetShare
	if share < l.tokens {
		l.tokens = share
	}
	window := time.Until(reset)
	if reset.IsZero() || window <= 0 {
		return
	}
	switch rate := share / window.Seconds(); {
	case rate < minRate:
		l.rate = minRate
	case rate > steadyRate:
		l.rate = steadyRate
	default:
		l.rate = rate
	}
}

// block parks every request against the key until the window resets, so one
// caller's 429 stops the others from spending the same exhausted budget.
func (l *limiter) block(reset time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tokens = 0
	if reset.After(l.blocked) {
		l.blocked = reset
	}
}

// rateLimitHeaders reads Linear's per-key budget headers: how many requests the
// window has left and when it resets, sent as a unix millisecond stamp. Either may
// be absent — reads outside a metered path do not carry them.
func rateLimitHeaders(h http.Header) (remaining int, reset time.Time, ok bool) {
	if ms, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Requests-Reset")), 10, 64); err == nil && ms > 0 {
		reset = time.UnixMilli(ms)
	}
	n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-RateLimit-Requests-Remaining")))
	if err != nil || n < 0 {
		return 0, reset, false
	}
	return n, reset, true
}

// rateLimitRefusal reports the rate-limit refusal a response carries, or nil.
// Linear answers either with a 429 or with a GraphQL RATELIMITED error, so both
// shapes have to be recognised.
func rateLimitRefusal(status int, gr graphResponse, reset time.Time) *RateLimitError {
	message := ""
	for _, e := range gr.Errors {
		if strings.EqualFold(e.Extensions.Code, "RATELIMITED") || strings.EqualFold(e.Extensions.Type, "ratelimited") {
			return &RateLimitError{Message: e.message(), ResetAt: reset}
		}
		if message == "" {
			message = e.message()
		}
	}
	if status != http.StatusTooManyRequests {
		return nil
	}
	return &RateLimitError{Message: message, ResetAt: reset}
}

// retryWait is how long to wait before retrying a rate-limited request, and
// whether waiting in place is worth it at all. A numeric Retry-After (seconds) is
// honoured verbatim, otherwise the window's own reset time is used, falling back
// to exponential backoff. A wait past maxBackoff belongs to the caller's backoff
// rather than a held-open request, so it reports false.
func retryWait(header string, reset time.Time, attempt int) (time.Duration, bool) {
	d := time.Duration(1<<attempt) * time.Second
	switch secs, err := strconv.Atoi(strings.TrimSpace(header)); {
	case err == nil && secs >= 0:
		d = time.Duration(secs) * time.Second
	case !reset.IsZero():
		d = time.Until(reset)
	}
	if d > maxBackoff {
		return 0, false
	}
	if d < 0 {
		d = 0
	}
	return d, true
}
