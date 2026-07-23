package tracker

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

var (
	_ Tracker       = (*Internal)(nil)
	_ TicketLister  = (*Internal)(nil)
	_ IssueDetailer = (*Internal)(nil)
	_ IssueStatuser = (*Internal)(nil)
	_ IssueParenter = (*Internal)(nil)
	_ IssueLabeler  = (*Internal)(nil)
)

type recordedTransition struct {
	id string
	t  hubclient.Transition
}

type fakeHub struct {
	issues      map[string]hubclient.Issue
	backlog     []hubclient.BacklogItem
	created     []hubclient.InternalDraft
	transitions []recordedTransition
	nextNum     int
}

func newFakeHub() *fakeHub {
	return &fakeHub{issues: map[string]hubclient.Issue{}, nextNum: 100}
}

func (f *fakeHub) InternalIssue(_ context.Context, _, id string) (hubclient.Issue, error) {
	iss, ok := f.issues[id]
	if !ok {
		return hubclient.Issue{}, hubclient.ErrNotFound
	}
	return iss, nil
}

func (f *fakeHub) Backlog(_ context.Context, _ string, _ hubclient.BacklogQuery) ([]hubclient.BacklogItem, error) {
	return f.backlog, nil
}

func (f *fakeHub) CreateInternalIssue(_ context.Context, _ string, d hubclient.InternalDraft) (hubclient.Issue, error) {
	f.created = append(f.created, d)
	f.nextNum++
	id := "LOOP-" + strconv.Itoa(f.nextNum)
	return hubclient.Issue{ID: id, Title: d.Title}, nil
}

func (f *fakeHub) TransitionInternalIssue(_ context.Context, _, id string, t hubclient.Transition) (hubclient.Issue, error) {
	f.transitions = append(f.transitions, recordedTransition{id: id, t: t})
	return hubclient.Issue{ID: id, State: t.State}, nil
}

func newInternal(hub hubAPI) *Internal {
	return &Internal{Hub: hub, Repo: "loop", ReadyLabel: "ready-for-agent", QuarantineLabel: "needs-human"}
}

func TestInternalPickSelectsLowestEligible(t *testing.T) {
	hub := newFakeHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "LOOP-10", Source: "internal", Group: "unstarted", Ready: true},
		{ID: "LOOP-2", Source: "internal", Group: "backlog", Ready: true},
		{ID: "LOOP-3", Source: "internal", Group: "started", Ready: true},         // already started
		{ID: "LOOP-4", Source: "internal", Group: "unstarted", HasChildren: true}, // epic
	}
	id, err := newInternal(hub).Pick(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "LOOP-2" {
		t.Fatalf("pick = %q, want LOOP-2 (lowest number among unstarted leaves)", id)
	}
}

func TestInternalPickSkipsBlocked(t *testing.T) {
	hub := newFakeHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "LOOP-2", Source: "internal", Group: "unstarted", Ready: true, Blocked: true},
		{ID: "LOOP-3", Source: "internal", Group: "unstarted", Ready: true},
	}
	id, err := newInternal(hub).Pick(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "LOOP-3" {
		t.Fatalf("pick = %q, want LOOP-3 (LOOP-2 has an unresolved blocker)", id)
	}
}

func TestInternalPickHonorsScopeParent(t *testing.T) {
	hub := newFakeHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "LOOP-2", Source: "internal", Group: "unstarted", Parent: ""},
		{ID: "LOOP-5", Source: "internal", Group: "unstarted", Parent: "LOOP-1"},
	}
	id, err := newInternal(hub).Pick(context.Background(), Scope{Parent: "LOOP-1"})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "LOOP-5" {
		t.Fatalf("pick = %q, want LOOP-5 (the child of the scoped epic)", id)
	}
}

func TestInternalPickNoneWhenEmpty(t *testing.T) {
	id, err := newInternal(newFakeHub()).Pick(context.Background(), Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "" {
		t.Fatalf("pick = %q, want empty", id)
	}
}

func TestInternalSetStatusMapsToState(t *testing.T) {
	hub := newFakeHub()
	in := newInternal(hub)
	if err := in.SetStatus(context.Background(), "LOOP-1", "In Review", "PR: http://x"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if len(hub.transitions) != 1 {
		t.Fatalf("transitions = %+v", hub.transitions)
	}
	got := hub.transitions[0]
	if got.t.State != "started" || got.t.Comment != "PR: http://x" {
		t.Fatalf("transition = %+v, want started + the PR comment", got.t)
	}
}

func TestInternalQuarantineSwapsLabels(t *testing.T) {
	hub := newFakeHub()
	if err := newInternal(hub).Quarantine(context.Background(), "LOOP-1", "verify dead end"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	tr := hub.transitions[0].t
	if !reflect.DeepEqual(tr.AddLabels, []string{"needs-human"}) || !reflect.DeepEqual(tr.RemoveLabels, []string{"ready-for-agent"}) {
		t.Fatalf("labels = +%v -%v, want +needs-human -ready-for-agent", tr.AddLabels, tr.RemoveLabels)
	}
	if tr.State != "" {
		t.Fatalf("state = %q, want unchanged on quarantine", tr.State)
	}
	if tr.Comment == "" {
		t.Fatal("want a quarantine comment pointing at the run artifacts")
	}
}

func TestInternalResetRestoresReady(t *testing.T) {
	hub := newFakeHub()
	if err := newInternal(hub).Reset(context.Background(), "LOOP-1"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	tr := hub.transitions[0].t
	if tr.State != "unstarted" || !reflect.DeepEqual(tr.AddLabels, []string{"ready-for-agent"}) || !reflect.DeepEqual(tr.RemoveLabels, []string{"needs-human"}) {
		t.Fatalf("reset transition = %+v", tr)
	}
}

func TestInternalFileBugCreatesInternalIssue(t *testing.T) {
	hub := newFakeHub()
	verdict := filepath.Join(t.TempDir(), "verdict.md")
	if err := os.WriteFile(verdict, []byte("FAILED: the widget did not render"), 0o644); err != nil {
		t.Fatalf("write verdict: %v", err)
	}
	id, err := newInternal(hub).FileBug(context.Background(), "LOOP-1", verdict)
	if err != nil {
		t.Fatalf("file bug: %v", err)
	}
	if id == "" || len(hub.created) != 1 {
		t.Fatalf("created = %+v, id = %q", hub.created, id)
	}
	d := hub.created[0]
	if !reflect.DeepEqual(d.Labels, []string{"HITL", "Bug"}) {
		t.Fatalf("labels = %v, want HITL+Bug", d.Labels)
	}
	if !strings.Contains(d.Description, "did not render") || !strings.Contains(d.Description, "LOOP-1") {
		t.Fatalf("description = %q, want the verdict and the ticket id", d.Description)
	}
}

func TestInternalIssueStatusMapsGroups(t *testing.T) {
	hub := newFakeHub()
	hub.issues["LOOP-1"] = hubclient.Issue{ID: "LOOP-1", State: "done"}
	hub.issues["LOOP-2"] = hubclient.Issue{ID: "LOOP-2", State: "started"}
	in := newInternal(hub)

	if st, _ := in.IssueStatus(context.Background(), "LOOP-1"); st != StatusDone {
		t.Fatalf("done status = %q, want done", st)
	}
	if st, _ := in.IssueStatus(context.Background(), "LOOP-2"); st != StatusStarted {
		t.Fatalf("started status = %q, want started", st)
	}
	if st, err := in.IssueStatus(context.Background(), "LOOP-9"); st != StatusUnknown || err != nil {
		t.Fatalf("missing status = %q err %v, want unknown/nil", st, err)
	}
}

func TestInternalSubIssuesFiltersByParent(t *testing.T) {
	hub := newFakeHub()
	hub.backlog = []hubclient.BacklogItem{
		{ID: "LOOP-2", Source: "internal", Parent: "LOOP-1", Group: "done"},
		{ID: "LOOP-3", Source: "internal", Parent: "LOOP-1", Group: "unstarted"},
		{ID: "LOOP-4", Source: "internal", Parent: "LOOP-9"},
	}
	subs, err := newInternal(hub).SubIssues(context.Background(), "LOOP-1")
	if err != nil {
		t.Fatalf("sub issues: %v", err)
	}
	if len(subs) != 2 || subs[0].ID != "LOOP-2" || !subs[0].Done || subs[1].ID != "LOOP-3" {
		t.Fatalf("subs = %+v, want LOOP-2 (done) and LOOP-3 in order", subs)
	}
}

func TestInternalEnsureLabelsIsNoop(t *testing.T) {
	if err := newInternal(newFakeHub()).EnsureLabels(context.Background()); err != nil {
		t.Fatalf("ensure labels = %v, want nil no-op", err)
	}
}
