package tracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

var (
	_ Tracker        = (*StoreBacked)(nil)
	_ TicketLister   = (*StoreBacked)(nil)
	_ IssueDetailer  = (*StoreBacked)(nil)
	_ IssueStatuser  = (*StoreBacked)(nil)
	_ IssueParenter  = (*StoreBacked)(nil)
	_ IssueProjecter = (*StoreBacked)(nil)
	_ IssueLabeler   = (*StoreBacked)(nil)
	_ TeamLister     = (*StoreBacked)(nil)
)

type recordedMirror struct {
	id string
	m  hubclient.SyncedMirror
}

type fakeStoreHub struct {
	issues    map[string]hubclient.Issue
	backlog   []hubclient.BacklogItem
	created   []hubclient.InternalDraft
	mirrors   []recordedMirror
	syncErr   error
	mirrorErr error
	ops       []string
}

func newFakeStoreHub() *fakeStoreHub {
	return &fakeStoreHub{issues: map[string]hubclient.Issue{}}
}

func (f *fakeStoreHub) Sync(context.Context, string) error {
	f.ops = append(f.ops, "sync")
	return f.syncErr
}

func (f *fakeStoreHub) Issue(_ context.Context, _, id string) (hubclient.Issue, error) {
	iss, ok := f.issues[id]
	if !ok {
		return hubclient.Issue{}, hubclient.ErrNotFound
	}
	return iss, nil
}

func (f *fakeStoreHub) Backlog(_ context.Context, _ string, _ hubclient.BacklogQuery) ([]hubclient.BacklogItem, error) {
	f.ops = append(f.ops, "backlog")
	return f.backlog, nil
}

func (f *fakeStoreHub) MirrorSynced(_ context.Context, _, id string, m hubclient.SyncedMirror) error {
	f.mirrors = append(f.mirrors, recordedMirror{id: id, m: m})
	return f.mirrorErr
}

func (f *fakeStoreHub) CreateInternalIssue(_ context.Context, _ string, d hubclient.InternalDraft) (hubclient.Issue, error) {
	f.created = append(f.created, d)
	return hubclient.Issue{ID: "LOOP-1", Title: d.Title}, nil
}

// fakeWrites records the tracker writes StoreBacked delegates and flags any read it
// wrongly routes to the tracker instead of the store.
type fakeWrites struct {
	setStatus   []string
	resets      int
	quarantines int
	labels      []string
	ensured     int
	fileBugs    int
	teamCalls   int
	err         error
}

func (f *fakeWrites) Pick(context.Context, Scope) (string, error) {
	return "", errors.New("read via tracker")
}
func (f *fakeWrites) SubIssues(context.Context, string) ([]SubIssue, error) {
	return nil, errors.New("read via tracker")
}
func (f *fakeWrites) Title(context.Context, string) (string, error) {
	return "", errors.New("read via tracker")
}
func (f *fakeWrites) SetStatus(_ context.Context, id, status, _ string) error {
	f.setStatus = append(f.setStatus, id+":"+status)
	return f.err
}
func (f *fakeWrites) Reset(context.Context, string) error             { f.resets++; return f.err }
func (f *fakeWrites) Quarantine(_ context.Context, _, _ string) error { f.quarantines++; return f.err }
func (f *fakeWrites) FileBug(context.Context, string, string) (string, error) {
	f.fileBugs++
	return "EXT-9", nil
}
func (f *fakeWrites) EnsureLabels(context.Context) error { f.ensured++; return f.err }
func (f *fakeWrites) AddLabel(_ context.Context, _, label string) error {
	f.labels = append(f.labels, label)
	return f.err
}
func (f *fakeWrites) ListTeams(context.Context) ([]Team, error) {
	f.teamCalls++
	return []Team{{Key: "COD", Name: "Codesomelabs"}}, f.err
}

func newStoreBacked(hub storeHub, writes Tracker) *StoreBacked {
	return &StoreBacked{Writes: writes, Hub: hub, Repo: "acme", ReadyLabel: "ready-for-agent", QuarantineLabel: "needs-human"}
}

func TestStoreBackedPickNudgesSyncThenSelects(t *testing.T) {
	hub := newFakeStoreHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "COD-10", Source: "linear", Group: "unstarted", Ready: true},
		{ID: "COD-2", Source: "linear", Group: "backlog", Ready: true},
		{ID: "COD-3", Source: "linear", Group: "started", Ready: true},         // already started
		{ID: "COD-4", Source: "linear", Group: "unstarted", HasChildren: true}, // epic
	}
	id, err := newStoreBacked(hub, &fakeWrites{}).Pick(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "COD-2" {
		t.Fatalf("pick = %q, want COD-2 (lowest unstarted leaf)", id)
	}
	if !reflect.DeepEqual(hub.ops, []string{"sync", "backlog"}) {
		t.Fatalf("ops = %v, want a sync nudge before the store read", hub.ops)
	}
}

func TestStoreBackedListEligibleThreadsParent(t *testing.T) {
	hub := newFakeStoreHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "COD-806", Source: "linear", Group: "unstarted", Ready: true, Parent: "COD-805"},
		{ID: "COD-810", Source: "linear", Group: "backlog", Ready: true},
	}
	got, err := newStoreBacked(hub, &fakeWrites{}).ListEligible(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("ListEligible: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListEligible = %d tickets, want 2", len(got))
	}
	if got[0].ID != "COD-806" || got[0].Parent != "COD-805" {
		t.Errorf("sub-issue = %+v, want COD-806 with Parent COD-805", got[0])
	}
	if got[1].ID != "COD-810" || got[1].Parent != "" {
		t.Errorf("top-level = %+v, want COD-810 with empty Parent", got[1])
	}
}

func TestStoreBackedPickFailsWhenSyncFails(t *testing.T) {
	hub := newFakeStoreHub()
	hub.syncErr = errors.New("hub unreachable")
	if _, err := newStoreBacked(hub, &fakeWrites{}).Pick(context.Background(), Scope{}); err == nil {
		t.Fatal("pick error = nil, want the sync failure to surface so a stale pick is never made")
	}
}

func TestStoreBackedIssueDetailIncludesComments(t *testing.T) {
	hub := newFakeStoreHub()
	hub.issues["COD-1"] = hubclient.Issue{
		ID: "COD-1", Title: "Fix it", Description: "the body",
		Comments: []hubclient.Comment{{Author: "alice", Body: "first note"}, {Author: "bob", Body: "second note"}},
	}
	detail, err := newStoreBacked(hub, &fakeWrites{}).IssueDetail(context.Background(), "COD-1")
	if err != nil {
		t.Fatalf("issue detail: %v", err)
	}
	if detail.Title != "Fix it" || detail.Description != "the body" {
		t.Fatalf("detail = %+v, want the stored title/description", detail)
	}
	if len(detail.Comments) != 2 || detail.Comments[0].Author != "alice" || detail.Comments[1].Body != "second note" {
		t.Fatalf("comments = %+v, want both stored comments", detail.Comments)
	}
}

func TestStoreBackedSetStatusWritesTrackerAndMirrors(t *testing.T) {
	hub := newFakeStoreHub()
	writes := &fakeWrites{}
	if err := newStoreBacked(hub, writes).SetStatus(context.Background(), "COD-1", "In Progress", ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if !reflect.DeepEqual(writes.setStatus, []string{"COD-1:In Progress"}) {
		t.Fatalf("tracker writes = %v, want the status transition delegated", writes.setStatus)
	}
	if len(hub.mirrors) != 1 || hub.mirrors[0].m.Status != "In Progress" || hub.mirrors[0].m.StatusGroup != "started" {
		t.Fatalf("mirror = %+v, want the status+group mirrored to the store", hub.mirrors)
	}
}

func TestStoreBackedSetStatusMirrorFailureDoesNotFail(t *testing.T) {
	hub := newFakeStoreHub()
	hub.mirrorErr = errors.New("store write failed")
	// The tracker write is the source of truth; a failed store mirror must not fail
	// the transition (the next sync reconciles the row).
	if err := newStoreBacked(hub, &fakeWrites{}).SetStatus(context.Background(), "COD-1", "Done", ""); err != nil {
		t.Fatalf("set status = %v, want nil despite the mirror failure", err)
	}
}

func TestStoreBackedQuarantineMirrorsLabelSwap(t *testing.T) {
	hub := newFakeStoreHub()
	writes := &fakeWrites{}
	if err := newStoreBacked(hub, writes).Quarantine(context.Background(), "COD-1", "dead end"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if writes.quarantines != 1 {
		t.Fatalf("tracker quarantines = %d, want 1", writes.quarantines)
	}
	m := hub.mirrors[0].m
	if !reflect.DeepEqual(m.AddLabels, []string{"needs-human"}) || !reflect.DeepEqual(m.RemoveLabels, []string{"ready-for-agent"}) {
		t.Fatalf("mirror labels = +%v -%v, want +needs-human -ready-for-agent", m.AddLabels, m.RemoveLabels)
	}
}

func TestStoreBackedFileBugIsInternalNeverExternal(t *testing.T) {
	hub := newFakeStoreHub()
	writes := &fakeWrites{}
	verdict := filepath.Join(t.TempDir(), "verdict.md")
	if err := os.WriteFile(verdict, []byte("FAILED: nothing rendered"), 0o644); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	id, err := newStoreBacked(hub, writes).FileBug(context.Background(), "COD-1", verdict)
	if err != nil {
		t.Fatalf("file bug: %v", err)
	}
	if id != "LOOP-1" || len(hub.created) != 1 {
		t.Fatalf("filed bug = %q, created = %+v, want one internal issue", id, hub.created)
	}
	if writes.fileBugs != 0 {
		t.Fatal("the external tracker's FileBug was called — trau-filed bugs must be internal")
	}
	if !reflect.DeepEqual(hub.created[0].Labels, []string{"HITL", "Bug"}) {
		t.Fatalf("labels = %v, want HITL+Bug", hub.created[0].Labels)
	}
	if !strings.Contains(hub.created[0].Description, "nothing rendered") {
		t.Fatalf("description = %q, want the verdict body", hub.created[0].Description)
	}
}

func TestStoreBackedListTeamsForwardsToWrites(t *testing.T) {
	hub := newFakeStoreHub()
	writes := &fakeWrites{}
	teams, err := newStoreBacked(hub, writes).ListTeams(context.Background())
	if err != nil {
		t.Fatalf("list teams: %v", err)
	}
	if writes.teamCalls != 1 {
		t.Fatalf("tracker ListTeams calls = %d, want the enumeration delegated to Writes", writes.teamCalls)
	}
	if len(teams) != 1 || teams[0].Key != "COD" {
		t.Fatalf("teams = %+v, want the wrapped tracker's teams", teams)
	}
}

func TestStoreBackedIssueProjectGuardsCrossProject(t *testing.T) {
	hub := newFakeStoreHub()
	hub.issues["COD-1"] = hubclient.Issue{ID: "COD-1", InProject: true, Project: "acme"}
	hub.issues["M4C-9"] = hubclient.Issue{ID: "M4C-9", InProject: false, Project: "M4C"}
	sb := newStoreBacked(hub, &fakeWrites{})

	if proj, _ := sb.IssueProject(context.Background(), "COD-1"); proj != "" {
		t.Fatalf("in-project = %q, want empty (the guard's no-op)", proj)
	}
	if proj, _ := sb.IssueProject(context.Background(), "M4C-9"); proj != "M4C" {
		t.Fatalf("cross-project = %q, want M4C so the guard refuses it", proj)
	}
}

func TestStoreBackedIssueStatusMapsGroups(t *testing.T) {
	hub := newFakeStoreHub()
	hub.issues["COD-1"] = hubclient.Issue{ID: "COD-1", Group: "completed"}
	hub.issues["COD-2"] = hubclient.Issue{ID: "COD-2", Group: "started"}
	hub.issues["COD-3"] = hubclient.Issue{ID: "COD-3", Group: "canceled"}
	sb := newStoreBacked(hub, &fakeWrites{})

	if st, _ := sb.IssueStatus(context.Background(), "COD-1"); st != StatusDone {
		t.Fatalf("completed status = %q, want done", st)
	}
	if st, _ := sb.IssueStatus(context.Background(), "COD-2"); st != StatusOpen {
		t.Fatalf("started status = %q, want open", st)
	}
	if st, _ := sb.IssueStatus(context.Background(), "COD-3"); st != StatusCanceled {
		t.Fatalf("canceled status = %q, want canceled", st)
	}
	if st, err := sb.IssueStatus(context.Background(), "COD-9"); st != StatusUnknown || err != nil {
		t.Fatalf("missing status = %q err %v, want unknown/nil", st, err)
	}
}
