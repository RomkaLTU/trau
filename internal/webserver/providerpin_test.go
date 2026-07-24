package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
)

func putProviderPin(t *testing.T, ts *httptest.Server, id string, body ProviderPinRequest) (*http.Response, IssueResponse) {
	t.Helper()
	res := putJSON(t, ts.URL+APIPrefix+"/repos/acme/issues/"+id+"/provider", body)
	var out IssueResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
	}
	return res, out
}

func TestProviderPinStoresAndClears(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})

	res, out := putProviderPin(t, ts, "COD-1", ProviderPinRequest{Provider: "codex"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.ProviderPin != "codex" {
		t.Fatalf("response provider_pin = %q, want codex", out.ProviderPin)
	}
	iss, _, err := s.stores.Issues().Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.Provider != "codex" {
		t.Fatalf("stored provider = %q, want the pin persisted", iss.Provider)
	}

	cleared, out := putProviderPin(t, ts, "COD-1", ProviderPinRequest{Provider: ""})
	defer func() { _ = cleared.Body.Close() }()
	if cleared.StatusCode != http.StatusOK {
		t.Fatalf("clear status = %d, want 200", cleared.StatusCode)
	}
	if out.ProviderPin != "" {
		t.Fatalf("response provider_pin = %q, want it cleared", out.ProviderPin)
	}
}

func TestProviderPinRejectsUnknownProvider(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})

	res, _ := putProviderPin(t, ts, "COD-1", ProviderPinRequest{Provider: "gpt"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unregistered provider", res.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(body["error"], "claude | codex | kimi") {
		t.Fatalf("error = %q, want it to name the registered providers", body["error"])
	}
	iss, _, err := s.stores.Issues().Find(root, "COD-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.Provider != "" {
		t.Fatalf("stored provider = %q, want the refused value never written", iss.Provider)
	}
}

func TestProviderPinUnknownIssueIsNotFound(t *testing.T) {
	_, _, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})

	res, _ := putProviderPin(t, ts, "COD-404", ProviderPinRequest{Provider: "codex"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestIssueReportsTheProviderInheritedFromItsEpic(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-1"},
	})
	if _, _, err := s.stores.Issues().SetProvider(root, "COD-1", "codex"); err != nil {
		t.Fatalf("pin the epic: %v", err)
	}
	if _, _, err := s.stores.Issues().SetProvider(root, "COD-3", "claude"); err != nil {
		t.Fatalf("pin COD-3: %v", err)
	}

	_, slice := getIssue(t, ts, "acme", "COD-2")
	if slice.ProviderPin != "" || slice.ProviderInherited != "codex" || slice.ProviderInheritedFrom != "COD-1" {
		t.Fatalf("COD-2 = %+v, want codex inherited from COD-1 with no pin of its own", slice)
	}

	_, pinned := getIssue(t, ts, "acme", "COD-3")
	if pinned.ProviderPin != "claude" || pinned.ProviderInherited != "codex" {
		t.Fatalf("COD-3 = %+v, want its own pin alongside what it would inherit", pinned)
	}

	_, epic := getIssue(t, ts, "acme", "COD-1")
	if epic.ProviderInherited != "" || epic.ProviderInheritedFrom != "" {
		t.Fatalf("COD-1 = %+v, want no inheritance on a parentless epic", epic)
	}
}

func TestIssueInheritanceStopsAtTheParent(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1", HasChildren: true},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-2"},
	})
	if _, _, err := s.stores.Issues().SetProvider(root, "COD-1", "codex"); err != nil {
		t.Fatalf("pin the grandparent: %v", err)
	}

	_, grandchild := getIssue(t, ts, "acme", "COD-3")
	if grandchild.ProviderInherited != "" || grandchild.ProviderInheritedFrom != "" {
		t.Fatalf("COD-3 = %+v, want a grandparent's pin never to reach it", grandchild)
	}
}

func TestQueueItemCarriesTheIssueProviderPin(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog"},
		{Identifier: "COD-2", StatusGroup: "backlog"},
	})
	q := s.stores.Queue(root)
	if _, err := q.Add(queue.Item{ID: "COD-1", Kind: queue.KindTicket}); err != nil {
		t.Fatalf("enqueue COD-1: %v", err)
	}
	if _, err := q.Add(queue.Item{ID: "COD-2", Kind: queue.KindTicket, Provider: "claude"}); err != nil {
		t.Fatalf("enqueue COD-2: %v", err)
	}
	if _, _, err := s.stores.Issues().SetProvider(root, "COD-1", "codex"); err != nil {
		t.Fatalf("pin COD-1: %v", err)
	}
	if _, _, err := s.stores.Issues().SetProvider(root, "COD-2", "kimi"); err != nil {
		t.Fatalf("pin COD-2: %v", err)
	}

	raw, res := getQueue(t, ts, "acme")
	defer func() { _ = raw.Body.Close() }()
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if res.Items[0].ProviderPin != "codex" || res.Items[0].Provider != "" {
		t.Fatalf("COD-1 = %+v, want the pin alone", res.Items[0])
	}
	if res.Items[1].ProviderPin != "kimi" || res.Items[1].Provider != "claude" {
		t.Fatalf("COD-2 = %+v, want both the one-shot and the pin reported", res.Items[1])
	}
}
