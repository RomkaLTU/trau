package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalIssueDecodesResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPrefix+"/repos/acme/issues/internal/ACME-1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, Issue{ID: "ACME-1", Title: "Add search", State: "started", Parent: "ACME-9"})
	}))
	defer ts.Close()

	iss, err := New(ts.URL, "").InternalIssue(context.Background(), "acme", "ACME-1")
	if err != nil {
		t.Fatalf("InternalIssue: %v", err)
	}
	if iss.ID != "ACME-1" || iss.Title != "Add search" || iss.State != "started" || iss.Parent != "ACME-9" {
		t.Fatalf("issue = %+v", iss)
	}
}

func TestInternalIssueNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "nope"})
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "").InternalIssue(context.Background(), "acme", "ACME-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBacklogSendsFilters(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("source") != "internal" || q.Get("label") != "ready-for-agent" {
			t.Errorf("query = %v, want the internal+ready filter", q)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items": []BacklogItem{{ID: "ACME-2", Group: "unstarted", Ready: true}},
		})
	}))
	defer ts.Close()

	items, err := New(ts.URL, "").Backlog(context.Background(), "acme", BacklogQuery{Source: "internal", Label: "ready-for-agent"})
	if err != nil {
		t.Fatalf("Backlog: %v", err)
	}
	if len(items) != 1 || items[0].ID != "ACME-2" {
		t.Fatalf("items = %+v", items)
	}
}

func TestTransitionSendsBodyAndBearer(t *testing.T) {
	var got Transition
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q, want the bearer token", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeJSON(w, http.StatusOK, Issue{ID: "ACME-1", State: "done"})
	}))
	defer ts.Close()

	iss, err := New(ts.URL, "secret").TransitionInternalIssue(context.Background(), "acme", "ACME-1", Transition{
		State: "done", AddLabels: []string{"needs-human"}, Comment: "done",
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if iss.State != "done" {
		t.Fatalf("issue = %+v", iss)
	}
	if got.State != "done" || len(got.AddLabels) != 1 || got.Comment != "done" {
		t.Fatalf("sent body = %+v", got)
	}
}

func TestNon2xxCarriesHubError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boom"})
	}))
	defer ts.Close()

	_, err := New(ts.URL, "").InternalIssue(context.Background(), "acme", "ACME-1")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a non-nil, non-ErrNotFound error", err)
	}
	if got := err.Error(); !strings.Contains(got, "boom") {
		t.Fatalf("err = %q, want it to carry the hub message", got)
	}
}

func TestIssueReadsStorePath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != apiPrefix+"/repos/acme/issues/COD-1" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, Issue{
			ID: "COD-1", Title: "Fix", Description: "body", Group: "unstarted", InProject: true,
			Comments: []Comment{{Author: "ada", Body: "note"}},
		})
	}))
	defer ts.Close()

	iss, err := New(ts.URL, "").Issue(context.Background(), "acme", "COD-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.Description != "body" || !iss.InProject || len(iss.Comments) != 1 {
		t.Fatalf("issue = %+v", iss)
	}
}

func TestMirrorSyncedPostsMutation(t *testing.T) {
	var got SyncedMirror
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/issues/COD-1" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeJSON(w, http.StatusOK, Issue{ID: "COD-1"})
	}))
	defer ts.Close()

	if err := New(ts.URL, "").MirrorSynced(context.Background(), "acme", "COD-1", SyncedMirror{Status: "Done", StatusGroup: "completed"}); err != nil {
		t.Fatalf("MirrorSynced: %v", err)
	}
	if got.Status != "Done" || got.StatusGroup != "completed" {
		t.Fatalf("sent mirror = %+v", got)
	}
}

func TestSyncPostsToRepo(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/sync" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"issues": 0})
	}))
	defer ts.Close()

	if err := New(ts.URL, "").Sync(context.Background(), "acme"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !called {
		t.Fatal("sync endpoint was not called")
	}
}

func TestResolvedPromptsKeepsOnlyOverrides(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != apiPrefix+"/repos/acme/prompts" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"repo": "acme",
			"prompts": []map[string]any{
				{"name": "build", "effective": "repo", "effective_body": "custom build {{.ID}}"},
				{"name": "commit", "effective": "global", "effective_body": "custom commit {{.ID}}"},
				{"name": "verify", "effective": "default", "effective_body": "built-in verify body"},
			},
		})
	}))
	defer ts.Close()

	m, err := New(ts.URL, "").ResolvedPrompts(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ResolvedPrompts: %v", err)
	}
	want := map[string]string{"build": "custom build {{.ID}}", "commit": "custom commit {{.ID}}"}
	if len(m) != len(want) {
		t.Fatalf("map = %v, want %v", m, want)
	}
	for name, body := range want {
		if m[name] != body {
			t.Errorf("m[%q] = %q, want %q", name, m[name], body)
		}
	}
}

func TestResolvedPromptsUnreachable(t *testing.T) {
	if _, err := New("http://127.0.0.1:1", "").ResolvedPrompts(context.Background(), "acme"); !IsUnreachable(err) {
		t.Fatalf("err = %v, want an unreachable transport error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
