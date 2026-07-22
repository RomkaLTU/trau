package webserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func qaServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.URL + APIPrefix + "/repos/acme/qa"
}

func createQAAccount(t *testing.T, base string, body QAAccountRequest) QAAccountView {
	t.Helper()
	res := postJSON(t, base+"/accounts", body)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create account status = %d, want 201", res.StatusCode)
	}
	var out QAAccountView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode created account: %v", err)
	}
	return out
}

func TestQAAccountCreateMasksSecret(t *testing.T) {
	_, base := qaServer(t)

	view := createQAAccount(t, base, QAAccountRequest{
		Label:       "admin",
		Username:    "admin@example.test",
		Secret:      "top-secret-pw",
		Description: "billing flows",
	})
	if !view.SecretSet {
		t.Error("created account reports secret_set=false")
	}

	res := postJSON(t, base+"/accounts", QAAccountRequest{Label: "admin", Secret: "x"})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("duplicate label status = %d, want 409", res.StatusCode)
	}
	_ = res.Body.Close()

	listRes, err := http.Get(base + "/accounts")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = listRes.Body.Close() }()
	raw, _ := io.ReadAll(listRes.Body)
	if strings.Contains(string(raw), "top-secret-pw") {
		t.Fatalf("list response leaked the secret: %s", raw)
	}
	var list []QAAccountView
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || !list[0].SecretSet || list[0].Username != "admin@example.test" {
		t.Fatalf("list = %+v, want one masked account with its username visible", list)
	}
}

// TestQAAccountSource covers the provenance the loop's capture path writes: an
// agent-captured account is accepted and reported as such, a create that names no
// source is manual, and an unknown value is rejected rather than stored.
func TestQAAccountSource(t *testing.T) {
	_, base := qaServer(t)

	captured := createQAAccount(t, base, QAAccountRequest{
		Label:    "seeded owner",
		Username: "owner@example.test",
		Secret:   "pw",
		Source:   "agent",
	})
	if captured.Source != "agent" {
		t.Errorf("created source = %q, want %q", captured.Source, "agent")
	}
	if manual := createQAAccount(t, base, QAAccountRequest{Label: "admin", Secret: "x"}); manual.Source != "manual" {
		t.Errorf("sourceless create = %q, want %q", manual.Source, "manual")
	}

	res := postJSON(t, base+"/accounts", QAAccountRequest{Label: "bogus", Source: "somewhere-else"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown source status = %d, want 400", res.StatusCode)
	}
	_ = res.Body.Close()

	listRes, err := http.Get(base + "/accounts")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = listRes.Body.Close() }()
	var list []QAAccountView
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 2 || list[0].Source != "manual" || list[1].Source != "agent" {
		t.Fatalf("list sources = %+v", list)
	}
}

func TestQAAccountUpdateKeepsSecretWhenBlank(t *testing.T) {
	_, base := qaServer(t)
	view := createQAAccount(t, base, QAAccountRequest{Label: "admin", Secret: "original"})

	res := patchJSON(t, base+"/accounts/"+strconv.FormatInt(view.ID, 10), QAAccountRequest{
		Label:       "admin",
		Description: "now with a description",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()

	rosterRes, err := http.Get(base + "/roster")
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	defer func() { _ = rosterRes.Body.Close() }()
	var roster QARosterResponse
	if err := json.NewDecoder(rosterRes.Body).Decode(&roster); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	if len(roster.Accounts) != 1 || roster.Accounts[0].Secret != "original" {
		t.Fatalf("blank-secret update lost the stored secret: %+v", roster.Accounts)
	}
	if roster.Accounts[0].Description != "now with a description" {
		t.Errorf("update did not persist the description: %+v", roster.Accounts[0])
	}
}

func TestQAAccountDelete(t *testing.T) {
	_, base := qaServer(t)
	view := createQAAccount(t, base, QAAccountRequest{Label: "admin", Secret: "x"})

	req, _ := http.NewRequest(http.MethodDelete, base+"/accounts/"+strconv.FormatInt(view.ID, 10), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", res.StatusCode)
	}

	getRes, _ := http.Get(base + "/accounts/" + strconv.FormatInt(view.ID, 10))
	_ = getRes.Body.Close()
	if getRes.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete status = %d, want 404", getRes.StatusCode)
	}
}

func TestQANotesRoundTrip(t *testing.T) {
	_, base := qaServer(t)

	res := putJSON(t, base+"/notes", QANotesRequest{Notes: "disposable admin via the seeder; delete after"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put notes status = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()

	getRes, err := http.Get(base + "/notes")
	if err != nil {
		t.Fatalf("get notes: %v", err)
	}
	defer func() { _ = getRes.Body.Close() }()
	var view QANotesView
	if err := json.NewDecoder(getRes.Body).Decode(&view); err != nil {
		t.Fatalf("decode notes: %v", err)
	}
	if view.Notes != "disposable admin via the seeder; delete after" {
		t.Errorf("notes = %q", view.Notes)
	}
}

func TestQARosterReturnsFullSecrets(t *testing.T) {
	_, base := qaServer(t)
	createQAAccount(t, base, QAAccountRequest{Label: "admin", Username: "admin@example.test", Secret: "full-secret", Description: "billing"})
	res := putJSON(t, base+"/notes", QANotesRequest{Notes: "login at /auth"})
	_ = res.Body.Close()

	rosterRes, err := http.Get(base + "/roster")
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	defer func() { _ = rosterRes.Body.Close() }()
	var roster QARosterResponse
	if err := json.NewDecoder(rosterRes.Body).Decode(&roster); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	if len(roster.Accounts) != 1 || roster.Accounts[0].Secret != "full-secret" {
		t.Fatalf("roster did not return the full secret: %+v", roster.Accounts)
	}
	if roster.Notes != "login at /auth" {
		t.Errorf("roster notes = %q", roster.Notes)
	}
}

func TestQAAccountUnknownRepo(t *testing.T) {
	ts, _ := qaServer(t)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/qa/accounts")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}
}
