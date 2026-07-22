package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
)

func deleteIssueResponse(t *testing.T, res *http.Response) DeleteIssueResponse {
	t.Helper()
	var out DeleteIssueResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestDeleteIssueRemovesItAndItsTraces(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog"},
		{Identifier: "COD-2", Title: "Neighbour", StatusGroup: "backlog"},
	})
	if _, err := s.stores.Grill().Create(hubstore.NewGrillSession{Repo: root, IssueID: "COD-1"}); err != nil {
		t.Fatalf("create grill session: %v", err)
	}
	if _, err := s.stores.Queue(root).Add(queue.Item{ID: "COD-1", Kind: queue.KindTicket}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	deleted := deleteIssueResponse(t, res).Deleted
	if want := []string{"COD-1"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted = %v, want %v", deleted, want)
	}

	if _, found, err := s.stores.Issues().Get(root, "COD-1"); err != nil || found {
		t.Errorf("COD-1: found=%v err=%v, want the row gone", found, err)
	}
	sessions, err := s.stores.Grill().List(root, "")
	if err != nil {
		t.Fatalf("list grill sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("grill sessions = %+v, want the issue's session gone", sessions)
	}
	items, err := s.stores.Queue(root).Load()
	if err != nil {
		t.Fatalf("load queue: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("queue = %+v, want the issue's entry gone", items)
	}
	if _, found, err := s.stores.Issues().Get(root, "COD-2"); err != nil || !found {
		t.Errorf("neighbour COD-2: found=%v err=%v, want it kept", found, err)
	}
}

func TestDeleteIssueCollectsOrphanedAttachmentBlobs(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})
	blobs := s.stores.Attachments().Blobs()
	sha, size, err := blobs.Put(strings.NewReader("png bytes"), 0)
	if err != nil {
		t.Fatalf("put blob: %v", err)
	}
	if _, err := s.stores.Attachments().Create(hubstore.Attachment{
		Repo:            root,
		IssueIdentifier: "COD-1",
		Source:          hubstore.AttachmentSourceUpload,
		Filename:        "shot.png",
		SHA256:          sha,
		SizeBytes:       size,
		State:           hubstore.AttachmentCached,
	}); err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if _, err := os.Stat(blobs.Path(sha)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("blob stat = %v, want the cached bytes collected", err)
	}
	rows, err := s.stores.Attachments().ForIssue(root, "COD-1")
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("attachments = %+v, want the rows gone", rows)
	}
}

func TestDeleteIssueListsTheWholeEpicFamily(t *testing.T) {
	_, _, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-1"},
	})
	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	deleted := deleteIssueResponse(t, res).Deleted
	if want := []string{"COD-1", "COD-2", "COD-3"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted = %v, want the epic and both children", deleted)
	}
}

func TestIssueChildCountMatchesWhatTheDeleteRemoves(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-1"},
	})
	if _, _, err := s.stores.Issues().SetArchived(root, "COD-3", true); err != nil {
		t.Fatalf("archive child: %v", err)
	}

	for _, entry := range backlogItems(t, ts) {
		if entry.ID == "COD-1" && (entry.ChildrenTotal == nil || *entry.ChildrenTotal != 1) {
			t.Fatalf("board children_total = %v, want 1 — the archived child is off the board", entry.ChildrenTotal)
		}
	}

	_, epic := getIssue(t, ts, "acme", "COD-1")
	if epic.Children != 2 {
		t.Errorf("children = %d, want 2 — an archived child is purged with the epic like any other", epic.Children)
	}
	if _, leaf := getIssue(t, ts, "acme", "COD-2"); leaf.Children != 0 {
		t.Errorf("leaf children = %d, want 0", leaf.Children)
	}

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	deleted := deleteIssueResponse(t, res).Deleted
	if len(deleted) != epic.Children+1 {
		t.Errorf("deleted %v, want the %d children the confirm named plus the epic itself", deleted, epic.Children)
	}
}

func TestDeleteIssueConflictsWithARunningMember(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
	})
	q := s.stores.Queue(root)
	if _, err := q.Add(queue.Item{ID: "COD-2", Kind: queue.KindTicket}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRunning("COD-2", 4242); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while a child runs", res.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["error"], "COD-2") {
		t.Errorf("error = %q, want it to name the running child", body["error"])
	}
	if _, found, err := s.stores.Issues().Get(root, "COD-1"); err != nil || !found {
		t.Errorf("COD-1: found=%v err=%v, want the refusal to change nothing", found, err)
	}
}

func TestDeleteIssueUnknownTargetsAreNotFound(t *testing.T) {
	_, _, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})
	for name, url := range map[string]string{
		"unknown identifier": ts.URL + APIPrefix + "/repos/acme/issues/COD-404",
		"unknown repo":       ts.URL + APIPrefix + "/repos/nope/issues/COD-1",
	} {
		res := doReq(t, http.MethodDelete, url, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, res.StatusCode)
		}
		_ = res.Body.Close()
	}
}

func TestDeletedIssueStaysGoneAcrossASync(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog"}})
	if got := backlogItems(t, ts); len(got) != 1 {
		t.Fatalf("backlog before the delete = %+v, want the seeded issue", got)
	}
	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	_ = res.Body.Close()

	if _, _, err := s.stores.Issues().Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if got := backlogItems(t, ts); len(got) != 0 {
		t.Errorf("backlog = %+v, want the deleted issue absent after a re-sync", got)
	}
}

func backlogItems(t *testing.T, ts *httptest.Server) []BacklogEntry {
	t.Helper()
	res := doReq(t, http.MethodGet, ts.URL+APIPrefix+"/repos/acme/backlog", nil)
	defer func() { _ = res.Body.Close() }()
	var out BacklogResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode backlog: %v", err)
	}
	return out.Items
}
