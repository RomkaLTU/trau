package linearapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCountIssuesReturnsNodeCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "CountIssues") {
			t.Errorf("unexpected query: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"id":"a"},{"id":"b"},{"id":"c"}]}}}`)
	}))
	defer srv.Close()

	c := New("lin_key")
	c.Endpoint = srv.URL
	n, err := c.CountIssues(context.Background())
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
}

func TestCountIssuesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New("bad_key")
	c.Endpoint = srv.URL
	if _, err := c.CountIssues(context.Background()); err != ErrUnauthorized {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestCountIssuesDisabledWithoutKey(t *testing.T) {
	if _, err := New("").CountIssues(context.Background()); err != ErrNotEnabled {
		t.Errorf("err = %v, want ErrNotEnabled", err)
	}
}
