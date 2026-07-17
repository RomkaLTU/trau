package hubstore

import (
	"reflect"
	"slices"
	"testing"
)

// archiveBoardFixture is two epics with a child each plus a standalone leaf, so
// the archive tests can hide a leaf, a whole epic family, or a single child.
func archiveBoardFixture() []Issue {
	return []Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog"},
		{Identifier: "COD-5", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-6", StatusGroup: "backlog", Parent: "COD-5"},
	}
}

func sortedIDs(issues []Issue) []string {
	ids := idsOf(issues)
	slices.Sort(ids)
	return ids
}

func TestUpsertLeavesArchivedIssueArchived(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archive"
	if _, _, err := s.Upsert(repo, "linear", []Issue{{Identifier: "COD-1", Title: "before", StatusGroup: "backlog"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, found, err := s.SetArchived(repo, "COD-1", true); err != nil || !found {
		t.Fatalf("SetArchived: found=%v err=%v", found, err)
	}

	if _, _, err := s.Upsert(repo, "linear", []Issue{{Identifier: "COD-1", Title: "after sync", StatusGroup: "started"}}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	iss, _, err := s.Get(repo, "COD-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if iss.ArchivedAt == "" {
		t.Fatal("archived_at cleared by sync; an archive must survive an upsert")
	}
	if iss.Title != "after sync" {
		t.Errorf("title = %q, want the synced content still applied", iss.Title)
	}
}

func TestArchiveSurvivesTombstoneAndRevival(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archive"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", StatusGroup: "backlog"},
		{Identifier: "COD-2", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-1", true); err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	if _, err := s.Reconcile(repo, []string{"COD-2"}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if _, err := s.Reconcile(repo, []string{"COD-1", "COD-2"}); err != nil {
		t.Fatalf("revive: %v", err)
	}

	iss, _, err := s.Get(repo, "COD-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if iss.DeletedAt != "" {
		t.Fatalf("revival should clear the tombstone, got %q", iss.DeletedAt)
	}
	if iss.ArchivedAt == "" {
		t.Fatal("revival cleared archived_at; the tombstone-revival path must leave archive state alone")
	}
}

func TestBacklogPageHidesArchivedLeaf(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archiveleaf"
	if _, _, err := s.Upsert(repo, "linear", archiveBoardFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-3", true); err != nil {
		t.Fatalf("archive leaf: %v", err)
	}

	live, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if slices.Contains(idsOf(live), "COD-3") {
		t.Errorf("live board = %v, want the archived leaf COD-3 hidden", idsOf(live))
	}

	archived, _, _, err := s.BacklogPage(repo, BacklogFilter{Archived: true})
	if err != nil {
		t.Fatalf("BacklogPage archived: %v", err)
	}
	if !reflect.DeepEqual(idsOf(archived), []string{"COD-3"}) {
		t.Errorf("archived view = %v, want exactly [COD-3]", idsOf(archived))
	}
}

func TestBacklogPageHidesArchivedEpicFamilyIncludingLateChild(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archiveepic"
	if _, _, err := s.Upsert(repo, "linear", archiveBoardFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-1", true); err != nil {
		t.Fatalf("archive epic: %v", err)
	}
	if _, _, err := s.Upsert(repo, "linear", []Issue{{Identifier: "COD-4", StatusGroup: "backlog", Parent: "COD-1"}}); err != nil {
		t.Fatalf("late child: %v", err)
	}

	live, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	for _, hidden := range []string{"COD-1", "COD-2", "COD-4"} {
		if slices.Contains(idsOf(live), hidden) {
			t.Errorf("live board = %v, want the archived epic family member %s hidden", idsOf(live), hidden)
		}
	}
	for _, kept := range []string{"COD-5", "COD-6"} {
		if !slices.Contains(idsOf(live), kept) {
			t.Errorf("live board = %v, want the un-archived family member %s kept", idsOf(live), kept)
		}
	}

	archived, _, _, err := s.BacklogPage(repo, BacklogFilter{Archived: true})
	if err != nil {
		t.Fatalf("BacklogPage archived: %v", err)
	}
	if got := sortedIDs(archived); !reflect.DeepEqual(got, []string{"COD-1", "COD-2", "COD-4"}) {
		t.Errorf("archived view = %v, want the whole archived family incl. the late child", got)
	}
}

func TestBacklogPageHidesIndividuallyArchivedChild(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archivechild"
	if _, _, err := s.Upsert(repo, "linear", archiveBoardFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-6", true); err != nil {
		t.Fatalf("archive child: %v", err)
	}

	live, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if slices.Contains(idsOf(live), "COD-6") {
		t.Errorf("live board = %v, want the individually-archived child COD-6 hidden", idsOf(live))
	}
	var epic Issue
	for _, iss := range live {
		if iss.Identifier == "COD-5" {
			epic = iss
		}
	}
	if epic.Identifier == "" {
		t.Fatal("epic COD-5 hidden; only its child was archived")
	}
	if epic.ChildrenTotal != 0 {
		t.Errorf("COD-5 child total = %d, want 0 — an archived child is off the board", epic.ChildrenTotal)
	}

	archived, _, _, err := s.BacklogPage(repo, BacklogFilter{Archived: true})
	if err != nil {
		t.Fatalf("BacklogPage archived: %v", err)
	}
	if !reflect.DeepEqual(idsOf(archived), []string{"COD-6"}) {
		t.Errorf("archived view = %v, want exactly the archived child [COD-6]", idsOf(archived))
	}
}

func TestArchivedCountCountsExplicitArchivesOnly(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archivecount"
	if _, _, err := s.Upsert(repo, "linear", archiveBoardFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n, err := s.ArchivedCount(repo); err != nil || n != 0 {
		t.Fatalf("ArchivedCount before = %d err %v, want 0", n, err)
	}
	if _, _, err := s.SetArchived(repo, "COD-1", true); err != nil {
		t.Fatalf("archive epic: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-3", true); err != nil {
		t.Fatalf("archive leaf: %v", err)
	}

	if n, err := s.ArchivedCount(repo); err != nil || n != 2 {
		t.Fatalf("ArchivedCount = %d err %v, want 2 — one row per archived epic/leaf, the family child not stamped", n, err)
	}
}

func TestBacklogPageArchivedIssueIsNotEligible(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/archiveeligible"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", StatusGroup: "unstarted", Labels: []string{"ready-for-agent"}},
		{Identifier: "COD-2", StatusGroup: "unstarted", Labels: []string{"ready-for-agent"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.SetArchived(repo, "COD-1", true); err != nil {
		t.Fatalf("archive: %v", err)
	}

	got, _, _, err := s.BacklogPage(repo, BacklogFilter{Source: "synced", Label: "ready-for-agent"})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if !reflect.DeepEqual(idsOf(got), []string{"COD-2"}) {
		t.Errorf("eligible-shaped page = %v, want the archived COD-1 absent so the picker never selects it", idsOf(got))
	}
}
