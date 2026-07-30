package linearapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rateLimitedServer answers the first refusals with status and body, then serves a
// viewer payload, recording how many requests it took. The client is keyed on the
// test's own name so one test's exhausted budget never paces another's.
func rateLimitedServer(t *testing.T, refusals int, status int, body string, headers map[string]string) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if int(calls.Add(1)) <= refusals {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"viewer":{"id":"u-1","name":"Ada"}}}`)
	}))
	t.Cleanup(srv.Close)
	c := New(t.Name())
	c.Endpoint = srv.URL
	return c, &calls
}

const rateLimitBody = `{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour.","extensions":{"code":"RATELIMITED","userPresentableMessage":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour."}}]}`

func TestDoRetriesRateLimitWithRetryAfter(t *testing.T) {
	c, calls := rateLimitedServer(t, 1, http.StatusTooManyRequests, rateLimitBody, map[string]string{"Retry-After": "0"})

	id, _, err := c.Viewer(context.Background())
	if err != nil {
		t.Fatalf("Viewer: %v", err)
	}
	if id != "u-1" {
		t.Fatalf("viewer id = %q, want the retry to have succeeded", id)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want the 429 retried once", got)
	}
}

func TestDoSurfacesTypedRateLimitErrorWithReset(t *testing.T) {
	reset := time.Now().Add(42 * time.Minute).Truncate(time.Second)
	c, calls := rateLimitedServer(t, 99, http.StatusTooManyRequests, rateLimitBody, map[string]string{
		"X-RateLimit-Requests-Remaining": "0",
		"X-RateLimit-Requests-Reset":     strconv.FormatInt(reset.UnixMilli(), 10),
	})

	_, _, err := c.Viewer(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a rate-limit error", err)
	}
	var limit *RateLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if !limit.ResetAt.Equal(reset) {
		t.Fatalf("reset = %v, want %v from the response headers", limit.ResetAt, reset)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want a reset an hour out surfaced instead of waited on", got)
	}
	if n := strings.Count(err.Error(), "Only 2500 requests"); n != 1 {
		t.Fatalf("error = %q, want the rate-limit text once, got it %d times", err.Error(), n)
	}
}

func TestDoDetectsRateLimitedGraphQLErrorWithout429(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
	c, _ := rateLimitedServer(t, 99, http.StatusOK, rateLimitBody, map[string]string{"X-RateLimit-Requests-Reset": reset})

	if _, _, err := c.Viewer(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want a RATELIMITED GraphQL error recognised without a 429", err)
	}
}

// A refusal parks the whole key: the next caller must be turned away without
// spending a request of its own, whichever client it was built from.
func TestRateLimitBlocksLaterRequestsOnTheSameKey(t *testing.T) {
	reset := time.Now().Add(30 * time.Minute)
	c, calls := rateLimitedServer(t, 99, http.StatusTooManyRequests, rateLimitBody, map[string]string{
		"X-RateLimit-Requests-Reset": strconv.FormatInt(reset.UnixMilli(), 10),
	})

	if _, _, err := c.Viewer(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want the first call rate limited", err)
	}
	spent := calls.Load()

	other := New(t.Name())
	other.Endpoint = c.Endpoint
	if _, _, err := other.Viewer(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want the shared budget to refuse the second client too", err)
	}
	if got := calls.Load(); got != spent {
		t.Fatalf("requests = %d, want no request spent while the budget is parked (was %d)", got, spent)
	}
}

func TestGraphErrorMessageDoesNotRepeatPresentableText(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		presenting string
		want       string
	}{
		{"identical", "Rate limit exceeded.", "Rate limit exceeded.", "Rate limit exceeded."},
		{"presentable extends message", "Argument Validation Error", "Argument Validation Error: labelIds should not be null.", "Argument Validation Error: labelIds should not be null."},
		{"distinct detail is appended", "Argument Validation Error", "labelIds should not be null.", "Argument Validation Error: labelIds should not be null."},
		{"no detail", "Entity not found", "", "Entity not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e graphError
			e.Message = tt.message
			e.Extensions.UserPresentableMessage = tt.presenting
			if got := e.message(); got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLimiterPacesOnceTheBurstIsSpent(t *testing.T) {
	l := newLimiter()
	for range burst {
		if d, err := l.reserve(); err != nil || d != 0 {
			t.Fatalf("reserve within the burst = %v/%v, want an immediate token", d, err)
		}
	}
	d, err := l.reserve()
	if err != nil {
		t.Fatalf("reserve past the burst: %v", err)
	}
	if d <= 0 {
		t.Fatalf("wait = %v, want the next request paced", d)
	}
}

func TestLimiterRefusesWhenTheWindowIsSpent(t *testing.T) {
	l := newLimiter()
	l.observe(0, time.Now().Add(time.Hour))
	for range burst {
		if _, err := l.reserve(); err != nil {
			break
		}
	}
	if _, err := l.reserve(); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("reserve = %v, want a rate-limit refusal once the reported budget is gone", err)
	}
}

func TestRateLimitHeadersReadRemainingAndReset(t *testing.T) {
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Millisecond)
	h := http.Header{}
	h.Set("X-RateLimit-Requests-Remaining", "1200")
	h.Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))

	remaining, got, ok := rateLimitHeaders(h)
	if !ok || remaining != 1200 {
		t.Fatalf("remaining = %d/%v, want 1200 read", remaining, ok)
	}
	if !got.Equal(reset) {
		t.Fatalf("reset = %v, want %v", got, reset)
	}
	if _, _, ok := rateLimitHeaders(http.Header{}); ok {
		t.Fatal("headerless response reported as metered")
	}
}

func TestRetryWaitHonoursRetryAfterAndGivesUpOnLongWindows(t *testing.T) {
	if d, ok := retryWait("2", time.Time{}, 0); !ok || d != 2*time.Second {
		t.Fatalf("retryWait(Retry-After 2) = %v/%v, want 2s honoured", d, ok)
	}
	if _, ok := retryWait("", time.Now().Add(time.Hour), 0); ok {
		t.Fatal("a window resetting an hour out should be left to the caller's backoff")
	}
	if d, ok := retryWait("", time.Time{}, 2); !ok || d != 4*time.Second {
		t.Fatalf("retryWait(attempt 2) = %v/%v, want exponential backoff", d, ok)
	}
}
