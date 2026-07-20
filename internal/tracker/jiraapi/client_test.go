package jiraapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewDisabledWithoutCredentials(t *testing.T) {
	cases := []struct {
		name                  string
		baseURL, email, token string
	}{
		{"all empty", "", "", ""},
		{"empty token", "https://acme.atlassian.net", "me@acme.com", ""},
		{"empty email", "https://acme.atlassian.net", "", "tok"},
		{"empty base url", "", "me@acme.com", "tok"},
	}
	for _, tc := range cases {
		c := New(tc.baseURL, tc.email, tc.token)
		if c.enabled() {
			t.Errorf("%s: client should be disabled", tc.name)
		}
		if _, err := c.Issue(context.Background(), "PROJ-1"); !errors.Is(err, ErrNotEnabled) {
			t.Errorf("%s: Issue err = %v, want ErrNotEnabled", tc.name, err)
		}
	}
}

func TestIssueSendsBasicAuthAndReturnsSummary(t *testing.T) {
	const email, token = "me@acme.com", "s3cr3t"
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token))

	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if got := r.URL.Query().Get("fields"); got != "summary,description,status,resolution,project,parent,labels,attachment" {
			t.Errorf("fields query = %q, want the widened field set", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-414","fields":{"summary":"Ship the thing","labels":["ready-for-agent"]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, email, token)
	issue, err := c.Issue(context.Background(), "PROJ-414")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if gotPath != "/rest/api/3/issue/PROJ-414" {
		t.Errorf("request path = %q, want /rest/api/3/issue/PROJ-414", gotPath)
	}
	if issue.Summary != "Ship the thing" {
		t.Errorf("summary = %q, want %q", issue.Summary, "Ship the thing")
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "ready-for-agent" {
		t.Errorf("labels = %v, want [ready-for-agent]", issue.Labels)
	}
}

func TestIssueMapsStatusToSentinel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"not found", http.StatusNotFound, ErrNotFound},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		c := New(srv.URL, "me@acme.com", "tok")
		_, err := c.Issue(context.Background(), "PROJ-1")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Issue err = %v, want %v", tc.name, err, tc.want)
		}
		srv.Close()
	}
}

func TestIssueMapsAllFields(t *testing.T) {
	const payload = `{
		"key":"PROJ-9",
		"fields":{
			"summary":"Do the thing",
			"description":{"type":"doc","version":1,"content":[
				{"type":"paragraph","content":[{"type":"text","text":"Line one."}]},
				{"type":"paragraph","content":[{"type":"text","text":"Line two."}]}
			]},
			"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			"resolution":{"name":"Done"},
			"project":{"key":"PROJ","name":"Project X","id":"10001"},
			"parent":{"key":"PROJ-1"}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	issue, err := New(srv.URL, "me@acme.com", "tok").Issue(context.Background(), "PROJ-9")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if issue.Summary != "Do the thing" {
		t.Errorf("Summary = %q, want %q", issue.Summary, "Do the thing")
	}
	if issue.Description != "Line one.\n\nLine two." {
		t.Errorf("Description = %q, want %q", issue.Description, "Line one.\n\nLine two.")
	}
	if issue.Status.Category != "indeterminate" || issue.Status.Name != "In Progress" {
		t.Errorf("Status = %+v, want {In Progress indeterminate}", issue.Status)
	}
	if issue.Resolution != "Done" {
		t.Errorf("Resolution = %q, want %q", issue.Resolution, "Done")
	}
	if issue.Project != (Project{Key: "PROJ", Name: "Project X", ID: "10001"}) {
		t.Errorf("Project = %+v, want {PROJ Project X 10001}", issue.Project)
	}
	if issue.Parent != "PROJ-1" {
		t.Errorf("Parent = %q, want %q", issue.Parent, "PROJ-1")
	}
}

// A minimal issue (no description/status/resolution/project/parent) must map to
// zero values without panicking on the absent optional objects.
func TestIssueHandlesMissingOptionalFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-2","fields":{"summary":"S","description":null}}`))
	}))
	defer srv.Close()

	issue, err := New(srv.URL, "me@acme.com", "tok").Issue(context.Background(), "PROJ-2")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}
	if issue.Description != "" || issue.Resolution != "" || issue.Parent != "" {
		t.Errorf("optional fields not empty: %+v", issue)
	}
	if issue.Status != (Status{}) || issue.Project != (Project{}) {
		t.Errorf("optional objects not zero: status=%+v project=%+v", issue.Status, issue.Project)
	}
}

// A numeric Retry-After is honoured verbatim; otherwise the wait is the capped
// exponential backoff plus at most 25% jitter, and never exceeds the cap+jitter.
func TestRetryAfter(t *testing.T) {
	if got := retryAfter("2", 0, 0.999); got != 2*time.Second {
		t.Errorf("numeric Retry-After = %v, want 2s (verbatim, no jitter)", got)
	}
	if got := retryAfter("  5 ", 3, 0.5); got != 5*time.Second {
		t.Errorf("padded Retry-After = %v, want 5s", got)
	}

	// attempt 2 → base 4s; jitter adds [0, 1s).
	if got := retryAfter("", 2, 0.0); got != 4*time.Second {
		t.Errorf("zero-jitter backoff = %v, want 4s", got)
	}
	base := 4 * time.Second
	max := base + base/4
	for _, j := range []float64{0.0, 0.25, 0.5, 0.75, 0.999} {
		got := retryAfter("", 2, j)
		if got < base || got >= max {
			t.Errorf("retryAfter jitter=%v = %v, want within [%v, %v)", j, got, base, max)
		}
	}

	// A large attempt is capped at maxBackoff before jitter is applied.
	if got := retryAfter("", 20, 0.0); got != maxBackoff {
		t.Errorf("capped backoff = %v, want %v", got, maxBackoff)
	}
	if got := retryAfter("", 20, 0.999); got < maxBackoff || got >= maxBackoff+maxBackoff/4 {
		t.Errorf("capped backoff with jitter = %v, want within [%v, %v)", got, maxBackoff, maxBackoff+maxBackoff/4)
	}
}

// AuthErrorMessage translates only ErrUnauthorized into an actionable regenerate
// hint carrying the token URL; every other error yields "" so callers surface
// their own message.
func TestAuthErrorMessage(t *testing.T) {
	msg := AuthErrorMessage(ErrUnauthorized)
	if !strings.Contains(msg, TokenHelpURL) {
		t.Errorf("AuthErrorMessage(ErrUnauthorized) = %q, want it to contain %q", msg, TokenHelpURL)
	}
	if wrapped := AuthErrorMessage(fmt.Errorf("call myself: %w", ErrUnauthorized)); wrapped == "" {
		t.Error("AuthErrorMessage should match a wrapped ErrUnauthorized")
	}
	for _, err := range []error{nil, ErrNotFound, ErrNotEnabled, errors.New("boom")} {
		if got := AuthErrorMessage(err); got != "" {
			t.Errorf("AuthErrorMessage(%v) = %q, want empty", err, got)
		}
	}
}

// Ping issues a single GET /myself with Basic auth, returning nil on 200,
// ErrUnauthorized on 401, and ErrNotEnabled when the client has no credentials.
func TestPing(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"abc123"}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "me@acme.com", "tok").Ping(context.Background()); err != nil {
		t.Fatalf("Ping error: %v", err)
	}
	if gotPath != "/rest/api/3/myself" {
		t.Errorf("Ping path = %q, want /rest/api/3/myself", gotPath)
	}

	un := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer un.Close()
	if err := New(un.URL, "me@acme.com", "tok").Ping(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Ping on 401 = %v, want ErrUnauthorized", err)
	}

	if err := New("", "", "").Ping(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Ping disabled = %v, want ErrNotEnabled", err)
	}
}

// Myself reads GET /myself and returns the token's accountId and display name,
// never matching on the email Atlassian privacy settings may hide.
func TestMyself(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accountId":"acc-1","displayName":"Ada Lovelace","emailAddress":"ada@acme.com"}`))
	}))
	defer srv.Close()

	id, name, err := New(srv.URL, "me@acme.com", "tok").Myself(context.Background())
	if err != nil {
		t.Fatalf("Myself: %v", err)
	}
	if gotPath != "/rest/api/3/myself" {
		t.Errorf("Myself path = %q, want /rest/api/3/myself", gotPath)
	}
	if id != "acc-1" || name != "Ada Lovelace" {
		t.Fatalf("myself = %q/%q, want acc-1/Ada Lovelace", id, name)
	}

	if _, _, err := New("", "", "").Myself(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Myself disabled = %v, want ErrNotEnabled", err)
	}
}

func TestADFToText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"nil", "", ""},
		{"null", "null", ""},
		{"malformed", "{not json", ""},
		{"single paragraph", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hello world"}]}]}`, "Hello world"},
		{"two paragraphs", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"P1"}]},{"type":"paragraph","content":[{"type":"text","text":"P2"}]}]}`, "P1\n\nP2"},
		{"hard break", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Line1"},{"type":"hardBreak"},{"type":"text","text":"Line2"}]}]}`, "Line1\nLine2"},
		{"bullet list", `{"type":"doc","content":[{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"A"}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"B"}]}]}]}]}`, "- A\n- B"},
		{"heading then paragraph", `{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"Title"}]},{"type":"paragraph","content":[{"type":"text","text":"Body"}]}]}`, "# Title\n\nBody"},
		{"marks rendered", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"see "},{"type":"text","text":"bold","marks":[{"type":"strong"}]},{"type":"text","text":" here"}]}]}`, "see **bold** here"},
	}
	for _, tc := range cases {
		if got := adfToText(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("%s: adfToText = %q, want %q", tc.name, got, tc.want)
		}
	}
}
