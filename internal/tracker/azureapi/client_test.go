package azureapi

import (
	"context"
	"encoding/base64"
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
		name        string
		orgURL, pat string
	}{
		{"all empty", "", ""},
		{"empty token", "https://dev.azure.com/acme", ""},
		{"empty org url", "", "pat"},
	}
	for _, tc := range cases {
		c := New(tc.orgURL, tc.pat)
		if c.enabled() {
			t.Errorf("%s: client should be disabled", tc.name)
		}
		if _, err := c.WorkItem(context.Background(), "Contoso", 1); !errors.Is(err, ErrNotEnabled) {
			t.Errorf("%s: WorkItem err = %v, want ErrNotEnabled", tc.name, err)
		}
	}
}

// The PAT rides in the password half of Basic auth with an empty username — the
// scheme Azure DevOps documents — so a client built with only a token still
// authenticates.
func TestWorkItemSendsBasicAuthWithEmptyUsername(t *testing.T) {
	const pat = "s3cr3t"
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+pat))

	var gotAuth, gotPath, gotVersion, gotExpand string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotVersion = r.URL.Query().Get("api-version")
		gotExpand = r.URL.Query().Get("$expand")
		_, _ = w.Write([]byte(`{"id":1234,"fields":{"System.Title":"Ship the thing"}}`))
	}))
	defer srv.Close()

	item, err := New(srv.URL, pat).WorkItem(context.Background(), "Contoso", 1234)
	if err != nil {
		t.Fatalf("WorkItem returned error: %v", err)
	}
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", gotAuth, wantAuth)
	}
	if gotPath != "/Contoso/_apis/wit/workitems/1234" {
		t.Errorf("path = %q, want /Contoso/_apis/wit/workitems/1234", gotPath)
	}
	if gotVersion != apiVersion {
		t.Errorf("api-version = %q, want %q", gotVersion, apiVersion)
	}
	if gotExpand != "relations" {
		t.Errorf("$expand = %q, want relations", gotExpand)
	}
	if item.Title != "Ship the thing" {
		t.Errorf("title = %q, want %q", item.Title, "Ship the thing")
	}
}

// A team project name may contain spaces, which must survive into the path as an
// escaped segment rather than splitting the URL.
func TestWorkItemEscapesProjectName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"id":7,"fields":{}}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "pat").WorkItem(context.Background(), "Fabrikam Fiber", 7); err != nil {
		t.Fatalf("WorkItem returned error: %v", err)
	}
	if gotPath != "/Fabrikam%20Fiber/_apis/wit/workitems/7" {
		t.Errorf("escaped path = %q, want /Fabrikam%%20Fiber/_apis/wit/workitems/7", gotPath)
	}
}

// A rejected PAT does not always arrive as a 401: Azure DevOps answers some routes
// with 203 and an HTML sign-in page, which must still read as an auth failure
// rather than a JSON parse error.
func TestDecodeMapsStatusToSentinel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, "", ErrUnauthorized},
		{"forbidden", http.StatusForbidden, "", ErrUnauthorized},
		{"sign-in page", http.StatusNonAuthoritativeInfo, "<html>sign in</html>", ErrUnauthorized},
		{"not found", http.StatusNotFound, "", ErrNotFound},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := New(srv.URL, "pat").WorkItem(context.Background(), "Contoso", 1)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		srv.Close()
	}
}

func TestDecodeSurfacesAzureErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"TF401232: Work item 9 does not exist","typeKey":"WorkItemObjectNotFoundException"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "pat").WorkItem(context.Background(), "Contoso", 9)
	if err == nil {
		t.Fatal("WorkItem returned no error")
	}
	if want := "TF401232: Work item 9 does not exist"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to carry %q", err, want)
	}
}

func TestPingUsesCheapProjectRead(t *testing.T) {
	var gotPath, gotTop string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTop = r.URL.Query().Get("$top")
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()

	if err := New(srv.URL, "pat").Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if gotPath != "/_apis/projects" {
		t.Errorf("path = %q, want /_apis/projects", gotPath)
	}
	if gotTop != "1" {
		t.Errorf("$top = %q, want 1", gotTop)
	}
}

func TestPingWithoutCredentialsIsNotEnabled(t *testing.T) {
	if err := New("", "").Ping(context.Background()); !errors.Is(err, ErrNotEnabled) {
		t.Errorf("Ping err = %v, want ErrNotEnabled", err)
	}
}

func TestListProjectsSkipsUnnamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":[{"id":"g1","name":"Contoso"},{"id":"g2","name":""},{"id":"g3","name":"Fabrikam"}]}`))
	}))
	defer srv.Close()

	projects, err := New(srv.URL, "pat").ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(projects), projects)
	}
	if projects[0].Name != "Contoso" || projects[1].Name != "Fabrikam" {
		t.Errorf("projects = %+v, want Contoso and Fabrikam", projects)
	}
}

func TestAuthErrorMessageForOnlyAnswersUnauthorized(t *testing.T) {
	if msg := AuthErrorMessageFor(ErrNotFound, "https://dev.azure.com/acme"); msg != "" {
		t.Errorf("AuthErrorMessageFor(ErrNotFound) = %q, want empty", msg)
	}
	msg := AuthErrorMessageFor(ErrUnauthorized, "https://dev.azure.com/acme")
	if !strings.Contains(msg, "https://dev.azure.com/acme") {
		t.Errorf("message = %q, want it to echo the organization URL", msg)
	}
	if !strings.Contains(msg, TokenHelpURL) {
		t.Errorf("message = %q, want it to point at %s", msg, TokenHelpURL)
	}
	if !strings.Contains(msg, requiredScopes) {
		t.Errorf("message = %q, want it to name the %s scopes", msg, requiredScopes)
	}
}

// The hint answers a wrapped rejection, and never echoes what the failing call
// carried — a token above all.
func TestAuthErrorMessageNeverEchoesToken(t *testing.T) {
	msg := AuthErrorMessageFor(fmt.Errorf("ping as s3cr3t: %w", ErrUnauthorized), "https://dev.azure.com/acme")
	if msg == "" {
		t.Fatal("AuthErrorMessageFor should answer a wrapped ErrUnauthorized")
	}
	if strings.Contains(msg, "s3cr3t") {
		t.Errorf("message = %q, want no token", msg)
	}
}

func TestWithAPIVersionAppendsToExistingQuery(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/_apis/projects", "/_apis/projects?api-version=" + apiVersion},
		{"/_apis/projects?$top=1", "/_apis/projects?$top=1&api-version=" + apiVersion},
		{"/c/_apis/wit/workitems/1/comments?api-version=7.1-preview.4", "/c/_apis/wit/workitems/1/comments?api-version=7.1-preview.4"},
	}
	for _, tc := range cases {
		if got := withAPIVersion(tc.path); got != tc.want {
			t.Errorf("withAPIVersion(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestRetryAfterHonoursHeaderThenBacksOff(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		attempt int
		jitter  float64
		want    time.Duration
	}{
		{"numeric header wins", "7", 3, 0.9, 7 * time.Second},
		{"no header backs off", "", 0, 0, time.Second},
		{"no header doubles", "", 2, 0, 4 * time.Second},
		{"jitter adds a quarter", "", 0, 1, time.Second + 250*time.Millisecond},
		{"caps at maxBackoff", "", 20, 0, maxBackoff},
	}
	for _, tc := range cases {
		if got := retryAfter(tc.header, tc.attempt, tc.jitter); got != tc.want {
			t.Errorf("%s: retryAfter(%q, %d, %v) = %v, want %v", tc.name, tc.header, tc.attempt, tc.jitter, got, tc.want)
		}
	}
}

func TestSendRetriesOn429(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":5,"fields":{"System.Title":"Retried"}}`))
	}))
	defer srv.Close()

	item, err := New(srv.URL, "pat").WorkItem(context.Background(), "Contoso", 5)
	if err != nil {
		t.Fatalf("WorkItem returned error: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if item.Title != "Retried" {
		t.Errorf("title = %q, want Retried", item.Title)
	}
}
