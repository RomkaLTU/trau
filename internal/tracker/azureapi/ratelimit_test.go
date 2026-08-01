package azureapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// closeTo compares reset times with a tolerance: a delta-seconds Retry-After is
// resolved against the moment the response was decoded, not the moment the test
// built its expectation.
func closeTo(got, want time.Time) bool {
	if got.IsZero() || want.IsZero() {
		return got.Equal(want)
	}
	return got.After(want.Add(-2*time.Second)) && got.Before(want.Add(2*time.Second))
}

// A 429 the retry ladder cannot outlast surfaces as a typed refusal carrying when
// the organization said the budget refills, so the caller waits the throttle out
// instead of recording a failure nobody can fix.
func TestExhaustedRetriesSurfaceTypedRateLimitError(t *testing.T) {
	httpDate := time.Now().Add(90 * time.Second).UTC().Truncate(time.Second)
	cases := []struct {
		name   string
		header string
		want   time.Time
	}{
		{"delta-seconds", "45", time.Now().Add(45 * time.Second)},
		{"http-date", httpDate.Format(http.TimeFormat), httpDate},
		{"no header", "", time.Time{}},
		{"unreadable header", "soon", time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				header := tc.header
				if calls <= maxRetries {
					header = "0"
				}
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()

			err := New(srv.URL, "pat").Ping(context.Background())
			var limit *RateLimitError
			if !errors.As(err, &limit) {
				t.Fatalf("err = %v, want *RateLimitError", err)
			}
			if !errors.Is(err, ErrRateLimited) {
				t.Errorf("err = %v, want it to still match ErrRateLimited", err)
			}
			if calls != maxRetries+1 {
				t.Errorf("calls = %d, want the retry ladder exhausted (%d)", calls, maxRetries+1)
			}
			if !closeTo(limit.ResetAt, tc.want) {
				t.Errorf("ResetAt = %v, want %v", limit.ResetAt, tc.want)
			}
		})
	}
}
