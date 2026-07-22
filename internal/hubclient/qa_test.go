package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCreateQAAccountPostsToRepoRoster(t *testing.T) {
	var got QAAccountInput
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/qa/accounts" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	in := QAAccountInput{Label: "seeded owner", Username: "owner@example.test", Secret: "pw", Description: "seeders", Source: QASourceAgent}
	if err := New(ts.URL, "").CreateQAAccount(context.Background(), "acme", in); err != nil {
		t.Fatalf("CreateQAAccount: %v", err)
	}
	if got != in {
		t.Errorf("hub received %+v, want %+v", got, in)
	}
}

func TestCreateQAAccountSurfacesLabelConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": `a QA account labelled "admin" already exists`})
	}))
	defer ts.Close()

	err := New(ts.URL, "").CreateQAAccount(context.Background(), "acme", QAAccountInput{Label: "admin"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateQAAccount err = %v, want the hub's conflict message", err)
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
