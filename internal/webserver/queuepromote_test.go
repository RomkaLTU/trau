package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
)

// promoteHub is a queue hub whose store holds one synced epic and its two
// children — the family a promotion collapses — with the fake's capture standing
// in for the epic preview both children are still to do in.
func promoteHub(t *testing.T) (*Server, *fakeSupervisor, string, *httptest.Server) {
	t.Helper()
	s, fake, root, ts := queueHub(t, "acme")
	store := s.stores.Issues()
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-10", Title: "An epic", StatusGroup: "backlog", HasChildren: true})
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-11", Title: "First", StatusGroup: "backlog", Parent: "COD-10"})
	seedSyncedIssue(t, store, root, hubstore.Issue{Identifier: "COD-12", Title: "Second", StatusGroup: "backlog", Parent: "COD-10"})
	fake.captureOut = []byte(`[{"id":"COD-11","title":"First","state":"todo"},{"id":"COD-12","title":"Second","state":"todo"}]`)
	return s, fake, root, ts
}

func enqueueTicket(t *testing.T, ts *httptest.Server, req QueueRequest) QueueResponse {
	t.Helper()
	req.Kind = "ticket"
	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/queue", req)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("enqueue %s = %d, want 201", req.ID, res.StatusCode)
	}
	var out QueueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestEnqueueLastChildPromotesToTheEpic(t *testing.T) {
	_, fake, _, ts := promoteHub(t)

	partial := enqueueTicket(t, ts, QueueRequest{ID: "COD-11"})
	if len(partial.Items) != 1 || partial.Items[0].Kind != "ticket" {
		t.Fatalf("items = %+v, want the lone child queued as a ticket", partial.Items)
	}
	if len(fake.captures) != 0 {
		t.Errorf("captures = %d, want none until the queue covers the whole epic", len(fake.captures))
	}

	out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12"})
	if len(out.Items) != 1 {
		t.Fatalf("items = %+v, want the children collapsed into one epic row", out.Items)
	}
	epic := out.Items[0]
	if epic.Kind != "epic" || epic.ID != "COD-10" || epic.Position != 1 {
		t.Errorf("item = %+v, want the COD-10 epic where its first child sat", epic)
	}
	if epic.Title != "An epic" || epic.Source != "linear" {
		t.Errorf("title/source = %q/%q, want the epic's stored title and source", epic.Title, epic.Source)
	}
	if len(epic.SubIssues) != 2 {
		t.Fatalf("sub_issues = %+v, want both children carried", epic.SubIssues)
	}
	if len(fake.captures) != 1 {
		t.Errorf("captures = %d, want the one epic preview the promotion read", len(fake.captures))
	}

	_, view := getQueue(t, ts, "acme")
	if len(view.Items) != 1 || view.Items[0].ID != "COD-10" {
		t.Errorf("queue view = %+v, want only the promoted epic", view.Items)
	}
}

// TestEnqueueChildrenIntoFolderRepoStayTickets holds a folder repo's queue to the
// rows it accepted: the epic itself is refused there, so completing the family must
// not collapse the children into an epic row the pipeline would then refuse to run.
func TestEnqueueChildrenIntoFolderRepoStayTickets(t *testing.T) {
	_, _, root, ts := promoteHub(t)
	if err := os.MkdirAll(filepath.Join(root, "api-a", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	enqueueTicket(t, ts, QueueRequest{ID: "COD-11"})
	out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12"})

	if len(out.Items) != 2 {
		t.Fatalf("items = %+v, want both children queued as their own tickets", out.Items)
	}
	for _, it := range out.Items {
		if it.Kind != "ticket" {
			t.Errorf("item = %+v, want a ticket row, not an epic a folder repo cannot run", it)
		}
	}
}

func TestEnqueuePromotionIgnoresChildrenAlreadyDone(t *testing.T) {
	for _, group := range []string{"done", "completed", "canceled"} {
		t.Run(group, func(t *testing.T) {
			s, fake, root, ts := promoteHub(t)
			seedSyncedIssue(t, s.stores.Issues(), root, hubstore.Issue{Identifier: "COD-11", Title: "First", StatusGroup: group, Parent: "COD-10"})
			fake.captureOut = []byte(`[{"id":"COD-11","title":"First","state":"done"},{"id":"COD-12","title":"Second","state":"todo"}]`)

			out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12"})
			if len(out.Items) != 1 || out.Items[0].ID != "COD-10" {
				t.Fatalf("items = %+v, want the epic promoted over its one open child", out.Items)
			}
			if len(out.Items[0].SubIssues) != 2 {
				t.Errorf("sub_issues = %+v, want the preview's children, done one included", out.Items[0].SubIssues)
			}
		})
	}
}

func TestEnqueuePromotionIgnoresAnArchivedChild(t *testing.T) {
	s, fake, root, ts := promoteHub(t)
	if _, found, err := s.stores.Issues().SetArchived(root, "COD-11", true); err != nil || !found {
		t.Fatalf("archive COD-11 = %v, %v", found, err)
	}

	out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12"})
	if len(out.Items) != 1 || out.Items[0].ID != "COD-10" {
		t.Fatalf("items = %+v, want the epic promoted over its one live child", out.Items)
	}
	if len(fake.captures) != 1 {
		t.Errorf("captures = %d, want the one epic preview the promotion read", len(fake.captures))
	}
}

func TestEnqueuePromotionIgnoresAnArchivedInternalChild(t *testing.T) {
	ts, root, store := internalIssueServer(t, true)
	_, epic := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Internal epic"})
	_, archived := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Archived child", Parent: epic.ID})
	_, open := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Open child", Parent: epic.ID})
	if _, found, err := store.SetArchived(root, archived.ID, true); err != nil || !found {
		t.Fatalf("archive %s = %v, %v", archived.ID, found, err)
	}

	out := enqueueTicket(t, ts, QueueRequest{ID: open.ID})
	if len(out.Items) != 1 || out.Items[0].ID != epic.ID {
		t.Fatalf("items = %+v, want the epic promoted over its one live child", out.Items)
	}
}

func TestEnqueueNoPromotionWhileAChildIsUnavailable(t *testing.T) {
	tests := []struct {
		name  string
		seeds []queue.Item
	}{
		{
			name:  "sibling running",
			seeds: []queue.Item{{Kind: queue.KindTicket, ID: "COD-11", Status: queue.StatusRunning, PID: 4242}},
		},
		{
			name:  "sibling paused",
			seeds: []queue.Item{{Kind: queue.KindTicket, ID: "COD-11", Status: queue.StatusPaused, Reason: "rate limited"}},
		},
		{
			name:  "sibling settled",
			seeds: []queue.Item{{Kind: queue.KindTicket, ID: "COD-11", Status: queue.StatusFailed, Reason: "fault"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, fake, root, ts := promoteHub(t)
			seedQueue(t, s, root, false, tt.seeds...)

			out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12"})
			if len(out.Items) != 2 {
				t.Fatalf("items = %+v, want both rows left alone", out.Items)
			}
			for _, it := range out.Items {
				if it.Kind == "epic" {
					t.Errorf("items = %+v, want no epic promoted over a child the queue cannot swap", out.Items)
				}
			}
			if len(fake.captures) != 0 {
				t.Errorf("captures = %d, want no epic preview for a family that cannot collapse", len(fake.captures))
			}
		})
	}
}

func TestEnqueuePromotedEpicKeepsAnAgreedProvider(t *testing.T) {
	tests := []struct {
		name     string
		second   string
		provider string
	}{
		{name: "children agree", second: "codex", provider: "codex"},
		{name: "children disagree", second: "claude", provider: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, ts := promoteHub(t)
			enqueueTicket(t, ts, QueueRequest{ID: "COD-11", Provider: "codex"})

			out := enqueueTicket(t, ts, QueueRequest{ID: "COD-12", Provider: tt.second})
			if len(out.Items) != 1 || out.Items[0].Kind != "epic" {
				t.Fatalf("items = %+v, want the promoted epic", out.Items)
			}
			if out.Items[0].Provider != tt.provider {
				t.Errorf("provider = %q, want %q", out.Items[0].Provider, tt.provider)
			}
		})
	}
}

func TestEnqueueLastInternalChildPromotesFromTheStore(t *testing.T) {
	ts, _, _ := internalIssueServer(t, true)
	_, epic := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Internal epic"})
	_, first := createInternal(t, ts, "acme", InternalIssueRequest{Title: "First child", Parent: epic.ID})
	_, second := createInternal(t, ts, "acme", InternalIssueRequest{Title: "Second child", Parent: epic.ID})

	enqueueTicket(t, ts, QueueRequest{ID: first.ID})
	out := enqueueTicket(t, ts, QueueRequest{ID: second.ID})

	if len(out.Items) != 1 {
		t.Fatalf("items = %+v, want the internal children collapsed into their epic", out.Items)
	}
	item := out.Items[0]
	if item.ID != epic.ID || item.Kind != "epic" || item.Source != "internal" {
		t.Errorf("item = %+v, want the %s internal epic", item, epic.ID)
	}
	if len(item.SubIssues) != 2 {
		t.Errorf("sub_issues = %+v, want both internal children", item.SubIssues)
	}
}
