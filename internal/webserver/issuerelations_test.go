package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

func relationsCall(t *testing.T, ts *httptest.Server, method, id string, body IssueRelationsRequest) (*http.Response, InternalIssueResponse) {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(method, ts.URL+APIPrefix+"/repos/acme/issues/"+id+"/relations", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s relations: %v", method, err)
	}
	var out InternalIssueResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode relations response: %v", err)
		}
	}
	return res, out
}

func TestIssueRelationsRoundTripsAndIsIdempotent(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	_, blocker := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Blocker"})
	_, dependent := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Dependent"})
	_, subject := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Subject"})

	body := IssueRelationsRequest{BlockedBy: []string{blocker.ID}, Blocks: []string{dependent.ID}}
	var out InternalIssueResponse
	for range 2 {
		res, got := relationsCall(t, ts, http.MethodPost, subject.ID, body)
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("POST status = %d, want 200", res.StatusCode)
		}
		out = got
	}
	if !reflect.DeepEqual(out.BlockedBy, []string{blocker.ID}) || !reflect.DeepEqual(out.Blocks, []string{dependent.ID}) {
		t.Fatalf("relations = %v / %v, want a single edge each way", out.BlockedBy, out.Blocks)
	}

	res, cleared := relationsCall(t, ts, http.MethodDelete, subject.ID, body)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200", res.StatusCode)
	}
	if len(cleared.BlockedBy) != 0 || len(cleared.Blocks) != 0 {
		t.Fatalf("relations after delete = %v / %v, want both cleared", cleared.BlockedBy, cleared.Blocks)
	}
}

func TestIssueRelationsRejectsUnknownTarget(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	_, subject := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Subject"})

	res, _ := relationsCall(t, ts, http.MethodPost, subject.ID, IssueRelationsRequest{BlockedBy: []string{"ACME-99"}})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if msg := decodeError(t, res); !strings.Contains(msg, "ACME-99") {
		t.Errorf("error = %q, want it to name the missing target", msg)
	}
}

func TestIssueRelationsRefusesSyncedIssue(t *testing.T) {
	ts, root, store := internalIssueServer(t, false)
	if _, _, err := store.Upsert(root, "linear", []hubstore.Issue{{Identifier: "ACME-7", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	res, _ := relationsCall(t, ts, http.MethodPost, "ACME-7", IssueRelationsRequest{Blocks: []string{"ACME-8"}})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a synced ticket's relations are the tracker's", res.StatusCode)
	}

	_, subject := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Subject"})
	target, _ := relationsCall(t, ts, http.MethodPost, subject.ID, IssueRelationsRequest{Blocks: []string{"ACME-7"}})
	defer func() { _ = target.Body.Close() }()
	if target.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — blocking a synced ticket writes its tracker-owned graph", target.StatusCode)
	}
	blockers, err := store.Blockers(root, "ACME-7")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("blockers of ACME-7 = %v, want the refused edge unwritten", blockers)
	}
}

func TestIssueRelationsUnknownIssue(t *testing.T) {
	ts, _, _ := internalIssueServer(t, false)
	res, _ := relationsCall(t, ts, http.MethodPost, "ACME-42", IssueRelationsRequest{Blocks: []string{"ACME-1"}})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func decodeError(t *testing.T, res *http.Response) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body["error"]
}
