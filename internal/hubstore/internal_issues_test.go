package hubstore

import (
	"errors"
	"reflect"
	"testing"
)

func TestCreateInternalAssignsSequentialIdentifiers(t *testing.T) {
	s := testIssues(t)
	a, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "First"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if a.Identifier != "LOOP-1" || a.Source != SourceInternal {
		t.Fatalf("a = %+v, want LOOP-1 internal", a)
	}
	if a.StatusGroup != "backlog" || a.Status != "Backlog" {
		t.Fatalf("a state = %q/%q, want the backlog default", a.StatusGroup, a.Status)
	}
	b, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Second"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if b.Identifier != "LOOP-2" {
		t.Fatalf("b id = %q, want LOOP-2", b.Identifier)
	}
}

func TestCreateInternalSkipsIdentifiersHeldBySyncedIssues(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/loop", "linear", []Issue{{Identifier: "LOOP-1", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Internal"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if iss.Identifier != "LOOP-2" {
		t.Fatalf("id = %q, want LOOP-2 — LOOP-1 is taken by a synced ticket", iss.Identifier)
	}
}

func TestCreateInternalStoresContent(t *testing.T) {
	s := testIssues(t)
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{
		Title:       "Add search",
		Description: "body",
		State:       "started",
		Labels:      []string{"ready-for-agent"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, found, err := s.Internal("/repo/loop", iss.Identifier)
	if err != nil || !found {
		t.Fatalf("Internal = found %v, err %v", found, err)
	}
	if got.Title != "Add search" || got.Description != "body" || got.StatusGroup != "started" || got.Status != "In Progress" {
		t.Fatalf("stored = %+v, want the created content", got)
	}
	if !reflect.DeepEqual(got.Labels, []string{"ready-for-agent"}) {
		t.Fatalf("labels = %v", got.Labels)
	}
}

func TestUpdateInternalReplacesFields(t *testing.T) {
	s := testIssues(t)
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := s.UpdateInternal("/repo/loop", iss.Identifier, InternalDraft{
		Title: "New", Description: "d2", State: "done", Labels: []string{"x"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != "New" || updated.StatusGroup != "done" {
		t.Fatalf("updated = %+v", updated)
	}
	got, _, _ := s.Internal("/repo/loop", iss.Identifier)
	if got.Title != "New" || got.Description != "d2" || got.StatusGroup != "done" {
		t.Fatalf("persisted = %+v, want the edit", got)
	}
}

func TestUpdateInternalRejectsSyncedIssue(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/loop", "linear", []Issue{{Identifier: "COD-1", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	_, err := s.UpdateInternal("/repo/loop", "COD-1", InternalDraft{Title: "hijack"})
	if !errors.Is(err, ErrInternalIssueNotFound) {
		t.Fatalf("err = %v, want ErrInternalIssueNotFound — a synced ticket is not editable here", err)
	}
	got, _ := s.List("/repo/loop")
	if len(got) != 1 || got[0].Title != "Synced" {
		t.Fatalf("synced issue = %+v, want it left unchanged", got)
	}
}

func TestSyncNeverOverwritesInternalIssue(t *testing.T) {
	s := testIssues(t)
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Local only", State: "started"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := s.Upsert("/repo/loop", "linear", []Issue{
		{Identifier: iss.Identifier, Title: "Tracker clobber", StatusGroup: "done"},
	}); err != nil {
		t.Fatalf("sync upsert: %v", err)
	}
	got, found, err := s.Internal("/repo/loop", iss.Identifier)
	if err != nil || !found {
		t.Fatalf("Internal = found %v, err %v", found, err)
	}
	if got.Source != SourceInternal || got.Title != "Local only" || got.StatusGroup != "started" {
		t.Fatalf("internal issue = %+v, want the sync to have left it untouched", got)
	}
}

func TestCreateInternalWithParentMarksEpic(t *testing.T) {
	s := testIssues(t)
	parent, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Epic"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "Child", Parent: parent.Identifier})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	got, _, _ := s.Internal("/repo/loop", parent.Identifier)
	if !got.HasChildren {
		t.Fatalf("parent = %+v, want has_children after nesting a child", got)
	}
	kids, err := s.InternalChildren("/repo/loop", parent.Identifier)
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(kids) != 1 || kids[0].Identifier != child.Identifier {
		t.Fatalf("children = %+v, want the one nested child", kids)
	}
}

func TestTransitionInternalAppliesStateLabelsAndComment(t *testing.T) {
	s := testIssues(t)
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{
		Title:  "Add search",
		State:  "unstarted",
		Labels: []string{"ready-for-agent"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.TransitionInternal("/repo/loop", iss.Identifier, InternalTransition{
		State:        "started",
		AddLabels:    []string{"needs-human"},
		RemoveLabels: []string{"ready-for-agent"},
		Comment:      "Trau loop stopped: verify dead end.",
	})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got.StatusGroup != "started" || got.Status != "In Progress" {
		t.Fatalf("state = %q/%q, want started/In Progress", got.StatusGroup, got.Status)
	}
	if !reflect.DeepEqual(got.Labels, []string{"needs-human"}) {
		t.Fatalf("labels = %v, want the ready label dropped and needs-human added", got.Labels)
	}
	persisted, _ := s.List("/repo/loop")
	if len(persisted) != 1 || len(persisted[0].Comments) != 1 {
		t.Fatalf("persisted = %+v, want one issue with one comment", persisted)
	}
	if c := persisted[0].Comments[0]; c.Author != internalCommentAuthor || c.Body != "Trau loop stopped: verify dead end." {
		t.Fatalf("comment = %+v, want the loop's authored body", c)
	}
}

func TestTransitionInternalLeavesStateWhenEmpty(t *testing.T) {
	s := testIssues(t)
	iss, err := s.CreateInternal("/repo/loop", "LOOP", InternalDraft{Title: "T", State: "started"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.TransitionInternal("/repo/loop", iss.Identifier, InternalTransition{AddLabels: []string{"x"}})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if got.StatusGroup != "started" {
		t.Fatalf("state = %q, want it left at started when no State is passed", got.StatusGroup)
	}
}

func TestTransitionInternalRejectsSyncedIssue(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/loop", "linear", []Issue{{Identifier: "COD-1", Title: "Synced"}}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	_, err := s.TransitionInternal("/repo/loop", "COD-1", InternalTransition{State: "done"})
	if !errors.Is(err, ErrInternalIssueNotFound) {
		t.Fatalf("err = %v, want ErrInternalIssueNotFound — a synced ticket is not written here", err)
	}
}

func TestMergeLabelsIsCaseInsensitive(t *testing.T) {
	got := mergeLabels([]string{"Ready-For-Agent", "feature"}, []string{"needs-human", "FEATURE"}, []string{"ready-for-agent"})
	if !reflect.DeepEqual(got, []string{"feature", "needs-human"}) {
		t.Fatalf("merged = %v, want ready dropped, feature deduped, needs-human added", got)
	}
}

func TestInternalIssuesAreRepoScoped(t *testing.T) {
	s := testIssues(t)
	a, err := s.CreateInternal("/repo/a", "LOOP", InternalDraft{Title: "A"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := s.CreateInternal("/repo/b", "LOOP", InternalDraft{Title: "B"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if a.Identifier != "LOOP-1" || b.Identifier != "LOOP-1" {
		t.Fatalf("ids = %q, %q, want each repo's own sequence to start at 1", a.Identifier, b.Identifier)
	}
	if _, found, _ := s.Internal("/repo/b", "LOOP-1"); !found {
		t.Fatal("repo b should own its own LOOP-1")
	}
}
