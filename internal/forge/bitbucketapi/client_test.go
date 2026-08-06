package bitbucketapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testClient wires a client at a loopback test server, the only host the network
// guard admits.
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return New("acme", "widgets", "rd@acme.com", "tok").WithBaseURL(ts.URL)
}

func TestNewDisablesWithoutEveryPartOfTheIdentity(t *testing.T) {
	cases := []struct {
		name                             string
		workspace, slug, email, apiToken string
		want                             bool
	}{
		{"complete", "acme", "widgets", "rd@acme.com", "tok", true},
		{"no token", "acme", "widgets", "rd@acme.com", "", false},
		{"no email", "acme", "widgets", "", "tok", false},
		{"no workspace", "", "widgets", "rd@acme.com", "tok", false},
		{"no slug", "acme", "", "rd@acme.com", "tok", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.workspace, tc.slug, tc.email, tc.apiToken).Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDisabledClientReturnsErrNotEnabled(t *testing.T) {
	c := New("acme", "widgets", "rd@acme.com", "")
	if _, err := c.Repo(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Repo on a disabled client = %v, want ErrNotEnabled", err)
	}
}

// TestRepoSendsBasicAuthAndReadsPrivacy proves the credential pair travels as
// HTTP Basic exactly as the Jira client sends it, and that the one field the PR
// body's proof links depend on is read back.
func TestRepoSendsBasicAuthAndReadsPrivacy(t *testing.T) {
	var gotAuth, gotPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`{"is_private":true,"mainbranch":{"name":"develop"}}`))
	})
	repo, err := c.Repo(context.Background())
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("rd@acme.com:tok"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotPath != "/repositories/acme/widgets" {
		t.Errorf("path = %q, want /repositories/acme/widgets", gotPath)
	}
	if !repo.Private || repo.MainBranch != "develop" {
		t.Errorf("repo = %+v, want private with mainbranch develop", repo)
	}
}

func TestDecodeMapsStatusesOntoSentinels(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"not found", http.StatusNotFound, ErrNotFound},
		{"rate limited", http.StatusTooManyRequests, ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			})
			// A 429 is retried before it surfaces; the ladder's waits are
			// sub-second for the first attempts, which keeps the test quick.
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := c.Ping(ctx)
			if tc.want == ErrRateLimited {
				if err == nil {
					t.Fatal("Ping = nil, want a rate-limit or deadline error")
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("Ping = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestErrorMessageLiftsBitbucketsOwnWording keeps a 400 carrying what Bitbucket
// said, which is the whole reason a create failure is actionable.
func TestErrorMessageLiftsBitbucketsOwnWording(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"branch not found","detail":"feature/x"}}`))
	})
	err := c.Ping(context.Background())
	if err == nil || !strings.Contains(err.Error(), "branch not found: feature/x") {
		t.Errorf("Ping = %v, want the message and detail Bitbucket sent", err)
	}
}

func TestAuthErrorMessageEchoesTheEmailAndNeverTheToken(t *testing.T) {
	msg := AuthErrorMessage(ErrUnauthorized, "rd@acme.com")
	if !strings.Contains(msg, "rd@acme.com") || !strings.Contains(msg, TokenHelpURL) {
		t.Errorf("hint = %q, want the account email and the token URL", msg)
	}
	// A 401 cannot tell an expired token from a mis-paired account, so the hint
	// has to name both rather than send the reader down one of them.
	if !strings.Contains(msg, "expired") || !strings.Contains(msg, "not the Atlassian account") {
		t.Errorf("hint = %q, want both causes named", msg)
	}
	if got := AuthErrorMessage(ErrNotFound, "rd@acme.com"); got != "" {
		t.Errorf("hint for a non-auth error = %q, want empty", got)
	}
}

func TestRetryAfterHonoursTheHeaderThenBacksOff(t *testing.T) {
	if got := retryAfter("5", 0, 0); got != 5*time.Second {
		t.Errorf("retryAfter with a header = %v, want 5s", got)
	}
	if got := retryAfter("", 2, 0); got != 4*time.Second {
		t.Errorf("retryAfter without a header = %v, want the 4s rung", got)
	}
	if got := retryAfter("", 10, 0); got != maxBackoff {
		t.Errorf("retryAfter far up the ladder = %v, want it capped at %v", got, maxBackoff)
	}
}
