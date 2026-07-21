package hubclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQARosterDecodesAccountsAndNotes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPrefix+"/repos/acme/qa/roster" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, QARoster{
			Accounts: []QAAccount{{Label: "admin", Username: "admin@example.test", Secret: "full-secret", Description: "billing"}},
			Notes:    "login at /auth",
		})
	}))
	defer ts.Close()

	roster, err := New(ts.URL, "").QARoster(context.Background(), "acme")
	if err != nil {
		t.Fatalf("QARoster: %v", err)
	}
	if len(roster.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(roster.Accounts))
	}
	a := roster.Accounts[0]
	if a.Label != "admin" || a.Secret != "full-secret" || a.Username != "admin@example.test" {
		t.Errorf("account = %+v", a)
	}
	if roster.Notes != "login at /auth" {
		t.Errorf("notes = %q", roster.Notes)
	}
}

func TestQARosterSendsBearer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		writeJSON(w, http.StatusOK, QARoster{})
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "tok").QARoster(context.Background(), "acme"); err != nil {
		t.Fatalf("QARoster: %v", err)
	}
}
