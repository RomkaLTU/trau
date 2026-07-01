package jiraapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
		if r.URL.Query().Get("fields") != "summary" {
			t.Errorf("fields query = %q, want summary", r.URL.Query().Get("fields"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"PROJ-414","fields":{"summary":"Ship the thing"}}`))
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
