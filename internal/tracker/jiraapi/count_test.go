package jiraapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountIssuesReturnsApproximateCount(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"count":42}`)
	}))
	defer srv.Close()

	n, err := New(srv.URL, "me@acme.com", "s3cr3t").CountIssues(context.Background())
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if n != 42 {
		t.Errorf("count = %d, want 42", n)
	}
	if gotPath != "/rest/api/3/search/approximate-count" {
		t.Errorf("path = %q, want /rest/api/3/search/approximate-count", gotPath)
	}
	if !strings.Contains(gotBody, `"jql"`) {
		t.Errorf("body = %q, want a jql field", gotBody)
	}
}

func TestCountIssuesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "e@x.com", "t").CountIssues(context.Background()); err != ErrUnauthorized {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestCountIssuesDisabledWithoutCredentials(t *testing.T) {
	if _, err := New("", "", "").CountIssues(context.Background()); err != ErrNotEnabled {
		t.Errorf("err = %v, want ErrNotEnabled", err)
	}
}
