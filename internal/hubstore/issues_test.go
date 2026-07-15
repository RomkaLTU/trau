package hubstore

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testIssues(t *testing.T) *Issues {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewIssues(db.SQL())
}

func sampleIssue() Issue {
	return Issue{
		Identifier:  "COD-1",
		Title:       "First",
		Description: "Body",
		Status:      "In Progress",
		StatusGroup: "started",
		Priority:    2,
		Labels:      []string{"ready-for-agent", "feature"},
		Parent:      "COD-9",
		HasChildren: false,
		DueDate:     "2026-08-01",
		ExternalID:  "iss-1",
		URL:         "https://linear.app/acme/issue/COD-1",
		Comments: []Comment{
			{ExternalID: "c1", Author: "Ada", Body: "looks good"},
		},
	}
}

func TestFindReturnsIssueWithComments(t *testing.T) {
	store := testIssues(t)
	if _, _, err := store.Upsert("acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	iss, found, err := store.Find("acme", "COD-1")
	if err != nil || !found {
		t.Fatalf("find: found=%v err=%v", found, err)
	}
	if iss.Description != "Body" {
		t.Errorf("description = %q, want the stored body", iss.Description)
	}
	if len(iss.Comments) != 1 || iss.Comments[0].Body != "looks good" {
		t.Errorf("comments = %+v, want the stored comment attached", iss.Comments)
	}
}

func TestFindMissReturnsNotFound(t *testing.T) {
	store := testIssues(t)
	if _, found, err := store.Find("acme", "COD-404"); found || err != nil {
		t.Fatalf("find miss = found %v err %v, want false/nil", found, err)
	}
}

func TestUpdateSyncedMirrorsStatusAndLabels(t *testing.T) {
	store := testIssues(t)
	if _, _, err := store.Upsert("acme", "linear", []Issue{{
		Identifier:  "COD-1",
		Title:       "Fix",
		Status:      "Todo",
		StatusGroup: "unstarted",
		Labels:      []string{"ready-for-agent"},
	}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	iss, found, err := store.UpdateSynced("acme", "COD-1", SyncedPatch{
		Status:       "In Progress",
		StatusGroup:  "started",
		AddLabels:    []string{"in-progress"},
		RemoveLabels: []string{"ready-for-agent"},
	})
	if err != nil || !found {
		t.Fatalf("update synced: found=%v err=%v", found, err)
	}
	if iss.Status != "In Progress" || iss.StatusGroup != "started" {
		t.Errorf("status/group = %q/%q, want In Progress/started", iss.Status, iss.StatusGroup)
	}
	if !reflect.DeepEqual(iss.Labels, []string{"in-progress"}) {
		t.Errorf("labels = %v, want the ready→in-progress swap", iss.Labels)
	}
}

func TestUpdateSyncedNeverTouchesInternal(t *testing.T) {
	store := testIssues(t)
	if _, err := store.CreateInternal("acme", "LOOP", InternalDraft{Title: "internal only"}); err != nil {
		t.Fatalf("create internal: %v", err)
	}
	if _, found, err := store.UpdateSynced("acme", "LOOP-1", SyncedPatch{StatusGroup: "started"}); found || err != nil {
		t.Fatalf("update synced on internal = found %v err %v, want false/nil (internal content is never mirrored)", found, err)
	}
}

func rawAssignee(t *testing.T, s *Issues, repo, identifier string) (id, name sql.NullString) {
	t.Helper()
	if err := s.db.QueryRow(
		`SELECT assignee_id, assignee_name FROM issues WHERE repo = ? AND identifier = ?`,
		repo, identifier,
	).Scan(&id, &name); err != nil {
		t.Fatalf("read assignee: %v", err)
	}
	return id, name
}

func TestUpsertStoresAssigneeAndClearsOnUnassignment(t *testing.T) {
	s := testIssues(t)

	assigned := sampleIssue()
	assigned.AssigneeID = "u-7"
	assigned.AssigneeName = "Ada Lovelace"
	if _, _, err := s.Upsert("acme", "linear", []Issue{assigned}); err != nil {
		t.Fatalf("upsert assigned: %v", err)
	}
	id, name := rawAssignee(t, s, "acme", "COD-1")
	if id.String != "u-7" || name.String != "Ada Lovelace" {
		t.Fatalf("assignee = %q/%q, want u-7/Ada Lovelace", id.String, name.String)
	}

	unassigned := sampleIssue()
	if _, _, err := s.Upsert("acme", "linear", []Issue{unassigned}); err != nil {
		t.Fatalf("upsert unassigned: %v", err)
	}
	id, name = rawAssignee(t, s, "acme", "COD-1")
	if id.Valid || name.Valid {
		t.Fatalf("assignee after unassignment = %v/%v, want NULL/NULL — the old assignee must not survive", id, name)
	}
}

func TestUpsertLeavesAbsentAssigneeNull(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id, name := rawAssignee(t, s, "acme", "COD-1"); id.Valid || name.Valid {
		t.Fatalf("assignee = %v/%v, want NULL for an unassigned issue", id, name)
	}
}

func TestSaveIdentityRoundTripPreservesBookkeeping(t *testing.T) {
	s := testIssues(t)
	if err := s.SaveBinding("acme", SyncBinding{TeamID: "t-1", ProjectID: "p-1"}); err != nil {
		t.Fatalf("save binding: %v", err)
	}
	if err := s.SaveIdentity("acme", "u-42", "Grace Hopper"); err != nil {
		t.Fatalf("save identity: %v", err)
	}
	st, err := s.SyncState("acme")
	if err != nil {
		t.Fatalf("sync state: %v", err)
	}
	if st.Me.ID != "u-42" || st.Me.Name != "Grace Hopper" || st.Me.ResolvedAt == "" {
		t.Fatalf("me = %+v, want u-42/Grace Hopper with a resolved-at stamp", st.Me)
	}
	if st.Binding.TeamID != "t-1" || st.Binding.ProjectID != "p-1" {
		t.Fatalf("binding = %+v, want the identity save to leave it intact", st.Binding)
	}
}

func TestUpsertStoresIssuesAndComments(t *testing.T) {
	s := testIssues(t)
	n, c, err := s.Upsert("/repo/acme", "linear", []Issue{sampleIssue()})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if n != 1 || c != 1 {
		t.Fatalf("counts = (%d, %d), want (1, 1)", n, c)
	}

	got, err := s.List("/repo/acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("issues = %d, want 1", len(got))
	}
	iss := got[0]
	if iss.Source != "linear" || iss.Identifier != "COD-1" || iss.Description != "Body" {
		t.Fatalf("issue = %+v, want linear COD-1 with body", iss)
	}
	if !reflect.DeepEqual(iss.Labels, []string{"ready-for-agent", "feature"}) {
		t.Fatalf("labels = %v", iss.Labels)
	}
	if len(iss.Comments) != 1 || iss.Comments[0].ExternalID != "c1" || iss.Comments[0].Author != "Ada" {
		t.Fatalf("comments = %+v, want one c1 from Ada", iss.Comments)
	}
}

func TestUpsertIsIdempotentByIdentifierAndCommentID(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{sampleIssue()}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	updated := sampleIssue()
	updated.Title = "First (edited)"
	updated.Status = "Done"
	updated.StatusGroup = "done"
	updated.Comments = []Comment{
		{ExternalID: "c1", Author: "Ada", Body: "revised"},
		{ExternalID: "c2", Author: "Bo", Body: "new one"},
	}
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{updated}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := s.List("/repo/acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("issues = %d, want 1 (upsert must not duplicate)", len(got))
	}
	if got[0].Title != "First (edited)" || got[0].Status != "Done" {
		t.Fatalf("issue not updated in place: %+v", got[0])
	}
	if len(got[0].Comments) != 2 {
		t.Fatalf("comments = %d, want 2 (c1 updated, c2 added)", len(got[0].Comments))
	}
	if got[0].Comments[0].Body != "revised" {
		t.Fatalf("comment c1 body = %q, want revised", got[0].Comments[0].Body)
	}
}

func TestIssuesAreScopedByRepo(t *testing.T) {
	s := testIssues(t)
	a := sampleIssue()
	b := sampleIssue()
	b.Identifier = "COD-2"
	if _, _, err := s.Upsert("/repo/a", "linear", []Issue{a}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	if _, _, err := s.Upsert("/repo/b", "linear", []Issue{b}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	if got, _ := s.List("/repo/a"); len(got) != 1 || got[0].Identifier != "COD-1" {
		t.Fatalf("repo a issues = %+v, want only COD-1", got)
	}
	if got, _ := s.List("/repo/b"); len(got) != 1 || got[0].Identifier != "COD-2" {
		t.Fatalf("repo b issues = %+v, want only COD-2", got)
	}
}

func seedBacklog(t *testing.T, s *Issues, repo string) {
	t.Helper()
	synced := []Issue{
		{Identifier: "COD-1", Title: "Login epic", Status: "Backlog", StatusGroup: "backlog", HasChildren: true, Labels: []string{"feature"}},
		{Identifier: "COD-2", Title: "Fix logout bug", Status: "Todo", StatusGroup: "unstarted", Labels: []string{"bug", "ready-for-agent"}},
		{Identifier: "COD-3", Title: "Dashboard polish", Status: "In Progress", StatusGroup: "started", Labels: []string{"Feature"}},
	}
	if _, _, err := s.Upsert(repo, "linear", synced); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	internal := []Issue{
		{Identifier: "COD-100", Title: "Internal login note", Status: "Todo", StatusGroup: "unstarted", Labels: []string{"chore"}},
	}
	if _, _, err := s.Upsert(repo, "internal", internal); err != nil {
		t.Fatalf("seed internal: %v", err)
	}
}

func idsOf(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, iss := range issues {
		out = append(out, iss.Identifier)
	}
	return out
}

func TestBacklogPageFilters(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/acme"
	seedBacklog(t, s, repo)

	tests := []struct {
		name   string
		filter BacklogFilter
		want   []string
	}{
		{"unfiltered", BacklogFilter{}, []string{"COD-3", "COD-2", "COD-100", "COD-1"}},
		{"state group", BacklogFilter{Groups: []string{"unstarted"}}, []string{"COD-2", "COD-100"}},
		{"source internal", BacklogFilter{Source: "internal"}, []string{"COD-100"}},
		{"source synced", BacklogFilter{Source: "synced"}, []string{"COD-3", "COD-2", "COD-1"}},
		{"label case-insensitive", BacklogFilter{Label: "feature"}, []string{"COD-3", "COD-1"}},
		{"text over id and title", BacklogFilter{Text: "login"}, []string{"COD-100", "COD-1"}},
		{"filters compose", BacklogFilter{Source: "synced", Text: "login"}, []string{"COD-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total, _, err := s.BacklogPage(repo, tt.filter)
			if err != nil {
				t.Fatalf("BacklogPage: %v", err)
			}
			if total != len(tt.want) {
				t.Errorf("total = %d, want %d", total, len(tt.want))
			}
			if !reflect.DeepEqual(idsOf(got), tt.want) {
				t.Errorf("ids = %v, want %v", idsOf(got), tt.want)
			}
		})
	}
}

func TestBacklogPagePaginates(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/acme"
	seedBacklog(t, s, repo)

	first, total, _, err := s.BacklogPage(repo, BacklogFilter{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if total != 4 {
		t.Fatalf("total = %d, want 4 (the full count, not the page size)", total)
	}
	if !reflect.DeepEqual(idsOf(first), []string{"COD-3", "COD-2"}) {
		t.Fatalf("first page = %v, want the first two in display order", idsOf(first))
	}

	second, _, _, err := s.BacklogPage(repo, BacklogFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !reflect.DeepEqual(idsOf(second), []string{"COD-100", "COD-1"}) {
		t.Fatalf("second page = %v, want the next two", idsOf(second))
	}
}

func TestBacklogPageTextEscapesWildcards(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/acme"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", Title: "100% coverage", StatusGroup: "backlog"},
		{Identifier: "COD-2", Title: "plain title", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, total, _, err := s.BacklogPage(repo, BacklogFilter{Text: "100%"})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if total != 1 || !reflect.DeepEqual(idsOf(got), []string{"COD-1"}) {
		t.Fatalf("ids = %v (total %d), want only COD-1 — the %% must match literally", idsOf(got), total)
	}
}

func TestBacklogPageGroupsFilter(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/groups"
	seedBacklog(t, s, repo)

	tests := []struct {
		name   string
		groups []string
		want   []string
	}{
		{"single group unchanged", []string{"unstarted"}, []string{"COD-2", "COD-100"}},
		{"union of groups", []string{"started", "unstarted"}, []string{"COD-3", "COD-2", "COD-100"}},
		{"blank entries dropped", []string{"", "started"}, []string{"COD-3"}},
		{"all blank means every group", []string{"", "  "}, []string{"COD-3", "COD-2", "COD-100", "COD-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total, _, err := s.BacklogPage(repo, BacklogFilter{Groups: tt.groups})
			if err != nil {
				t.Fatalf("BacklogPage: %v", err)
			}
			if total != len(tt.want) || !reflect.DeepEqual(idsOf(got), tt.want) {
				t.Errorf("ids = %v (total %d), want %v", idsOf(got), total, tt.want)
			}
		})
	}
}

func TestBacklogPageOrdersByGroupThenNumericIdentifier(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/order"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-2", StatusGroup: "started"},
		{Identifier: "COD-10", StatusGroup: "started"},
		{Identifier: "COD-9", StatusGroup: "unstarted"},
		{Identifier: "COD-100", StatusGroup: "unstarted"},
		{Identifier: "COD-3", StatusGroup: "backlog"},
		{Identifier: "COD-8", StatusGroup: "unknown"},
		{Identifier: "COD-5", StatusGroup: "done"},
		{Identifier: "COD-7", StatusGroup: "canceled"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	want := []string{"COD-2", "COD-10", "COD-9", "COD-100", "COD-3", "COD-8", "COD-5", "COD-7"}
	if !reflect.DeepEqual(idsOf(got), want) {
		t.Fatalf("order = %v, want group precedence then numeric-aware identifier %v", idsOf(got), want)
	}
}

func TestBacklogPageOrdersByFamilyThenIdentifier(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/family"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-873", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-875", StatusGroup: "backlog", Parent: "COD-873"},
		{Identifier: "COD-874", StatusGroup: "backlog", Parent: "COD-873"},
		{Identifier: "COD-9", StatusGroup: "backlog"},
		{Identifier: "COD-880", StatusGroup: "backlog"},
		{Identifier: "COD-877", StatusGroup: "started", Parent: "COD-873"},
		{Identifier: "COD-876", StatusGroup: "started", Parent: "COD-873"},
		{Identifier: "COD-3", StatusGroup: "started"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	want := []string{
		"COD-3", "COD-876", "COD-877",
		"COD-9", "COD-873", "COD-874", "COD-875", "COD-880",
	}
	if !reflect.DeepEqual(idsOf(got), want) {
		t.Fatalf("order = %v, want family-key order %v\n"+
			"expected: within a group, epics before their sub-issues, a sub-issue "+
			"immediately after its epic, sub-issues in another group clustered under "+
			"their epic key, and unrelated issues in numeric-aware order", idsOf(got), want)
	}
}

func TestBacklogPageCountsEpicChildren(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/childcounts"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "done", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "canceled", Parent: "COD-1"},
		{Identifier: "COD-4", StatusGroup: "started", Parent: "COD-1"},
		{Identifier: "COD-5", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-9", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	epic := func(issues []Issue) Issue {
		t.Helper()
		for _, iss := range issues {
			if iss.Identifier == "COD-1" {
				return iss
			}
		}
		t.Fatal("epic COD-1 absent from the page")
		return Issue{}
	}

	full, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if e := epic(full); e.ChildrenSettled != 2 || e.ChildrenTotal != 4 {
		t.Fatalf("epic counts = %d/%d, want 2/4 (done + canceled settled of four children)", e.ChildrenSettled, e.ChildrenTotal)
	}
	for _, iss := range full {
		if iss.Identifier != "COD-1" && (iss.ChildrenSettled != 0 || iss.ChildrenTotal != 0) {
			t.Errorf("non-epic %s carries counts %d/%d, want none", iss.Identifier, iss.ChildrenSettled, iss.ChildrenTotal)
		}
	}

	filtered, _, _, err := s.BacklogPage(repo, BacklogFilter{Groups: []string{"backlog"}})
	if err != nil {
		t.Fatalf("BacklogPage backlog-only: %v", err)
	}
	if e := epic(filtered); e.ChildrenSettled != 2 || e.ChildrenTotal != 4 {
		t.Fatalf("filtered epic counts = %d/%d, want 2/4 — counts cover all children, not the filtered page", e.ChildrenSettled, e.ChildrenTotal)
	}
}

func TestBacklogPageCountsIgnoreState(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/counts"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", Title: "a", StatusGroup: "started", Labels: []string{"feature"}},
		{Identifier: "COD-2", Title: "b", StatusGroup: "unstarted", Labels: []string{"feature"}},
		{Identifier: "COD-3", Title: "c", StatusGroup: "unstarted"},
		{Identifier: "COD-4", Title: "d", StatusGroup: "done", Labels: []string{"feature"}},
		{Identifier: "COD-5", Title: "e", StatusGroup: "canceled"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, total, counts, err := s.BacklogPage(repo, BacklogFilter{Groups: []string{"started"}})
	if err != nil {
		t.Fatalf("BacklogPage: %v", err)
	}
	if total != 1 || !reflect.DeepEqual(idsOf(got), []string{"COD-1"}) {
		t.Fatalf("page = %v (total %d), want only the started COD-1", idsOf(got), total)
	}
	wantCounts := map[string]int{"started": 1, "unstarted": 2, "done": 1, "canceled": 1}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("counts = %v, want every group counted despite the started-only page %v", counts, wantCounts)
	}

	_, _, labelled, err := s.BacklogPage(repo, BacklogFilter{Label: "feature"})
	if err != nil {
		t.Fatalf("BacklogPage label: %v", err)
	}
	if want := (map[string]int{"started": 1, "unstarted": 1, "done": 1}); !reflect.DeepEqual(labelled, want) {
		t.Fatalf("counts = %v, want only the feature-labelled rows per group %v", labelled, want)
	}
}

func TestLabelsFacet(t *testing.T) {
	s := testIssues(t)
	repo := "/repo/labels"
	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", Title: "a", StatusGroup: "backlog", Labels: []string{"Feature", "bug"}},
		{Identifier: "COD-2", Title: "b", StatusGroup: "unstarted", Labels: []string{"feature"}},
		{Identifier: "COD-3", Title: "c", StatusGroup: "started", Labels: []string{"bug", "ready-for-agent"}},
		{Identifier: "COD-4", Title: "d", StatusGroup: "backlog", Labels: []string{"stale"}},
	}); err != nil {
		t.Fatalf("seed synced: %v", err)
	}
	if _, _, err := s.Upsert(repo, "internal", []Issue{
		{Identifier: "COD-100", Title: "note", StatusGroup: "unstarted", Labels: []string{"feature", "chore"}},
	}); err != nil {
		t.Fatalf("seed internal: %v", err)
	}
	if _, err := s.Reconcile(repo, []string{"COD-1", "COD-2", "COD-3"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, err := s.Labels(repo)
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	want := []LabelCount{
		{Name: "bug", Count: 2},
		{Name: "chore", Count: 1},
		{Name: "Feature", Count: 3},
		{Name: "ready-for-agent", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %+v, want %+v\n"+
			"expected case-insensitive grouping (Feature/feature merged), distinct issue counts, "+
			"the tombstoned stale label excluded, and internal labels counted", got, want)
	}
}

func TestLabelsFacetEmptyRepo(t *testing.T) {
	s := testIssues(t)
	got, err := s.Labels("/repo/empty")
	if err != nil {
		t.Fatalf("Labels: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("labels = %+v, want an empty slice for a repo with no issues", got)
	}
}

func TestCount(t *testing.T) {
	s := testIssues(t)
	const repo = "/repo/acme"

	if n, err := s.Count(repo); err != nil || n != 0 {
		t.Fatalf("Count on empty store = %d, %v; want 0", n, err)
	}

	if _, _, err := s.Upsert(repo, "linear", []Issue{
		{Identifier: "COD-1", Title: "one"},
		{Identifier: "COD-2", Title: "two"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if n, err := s.Count(repo); err != nil || n != 2 {
		t.Fatalf("Count = %d, %v; want 2", n, err)
	}

	if _, err := s.Reconcile(repo, []string{"COD-1"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n, err := s.Count(repo); err != nil || n != 1 {
		t.Fatalf("Count after tombstoning = %d, %v; want 1 (tombstoned rows excluded)", n, err)
	}
}

func TestRecordErrorPreservesLastGoodSync(t *testing.T) {
	s := testIssues(t)

	if err := s.RecordResult("/repo/acme", SyncResult{
		Issues: 3, Comments: 5, Cursor: "2026-07-10T00:00:00Z", SyncedAt: "2026-07-11T00:00:00Z",
	}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	if err := s.RecordError("/repo/acme", "tracker: 401 unauthorized"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}

	st, err := s.SyncState("/repo/acme")
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError != "tracker: 401 unauthorized" {
		t.Fatalf("last error = %q, want the recorded failure", st.LastError)
	}
	if st.Cursor != "2026-07-10T00:00:00Z" || st.LastSyncedAt != "2026-07-11T00:00:00Z" || st.LastIssues != 3 {
		t.Fatalf("state = %+v, want the last good cursor/synced/counts preserved", st)
	}

	if err := s.RecordResult("/repo/acme", SyncResult{
		Issues: 4, Cursor: "2026-07-12T00:00:00Z", SyncedAt: "2026-07-12T00:00:00Z",
	}); err != nil {
		t.Fatalf("RecordResult after recovery: %v", err)
	}
	if st, _ := s.SyncState("/repo/acme"); st.LastError != "" {
		t.Fatalf("last error = %q, want cleared once a sync succeeds", st.LastError)
	}
}

func TestRecordErrorOnFirstSyncInserts(t *testing.T) {
	s := testIssues(t)
	if err := s.RecordError("/repo/acme", "boom"); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	st, err := s.SyncState("/repo/acme")
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.LastError != "boom" || st.LastSyncedAt != "" || st.Cursor != "" {
		t.Fatalf("state = %+v, want just the error on a never-synced repo", st)
	}
}

func TestSyncBindingAndResultRoundTrip(t *testing.T) {
	s := testIssues(t)

	if st, err := s.SyncState("/repo/acme"); err != nil || st.Binding.ProjectID != "" || st.LastSyncedAt != "" {
		t.Fatalf("fresh SyncState = %+v, %v, want zero value", st, err)
	}

	binding := SyncBinding{TeamID: "team-1", ProjectID: "proj-1", Project: "trau"}
	if err := s.SaveBinding("/repo/acme", binding); err != nil {
		t.Fatalf("SaveBinding: %v", err)
	}
	if err := s.RecordResult("/repo/acme", SyncResult{Issues: 3, Comments: 5, Cursor: "2026-07-10T00:00:00Z", SyncedAt: "2026-07-11T00:00:00Z"}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	st, err := s.SyncState("/repo/acme")
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if st.Binding != binding {
		t.Fatalf("binding = %+v, want %+v (recording a result must not clear it)", st.Binding, binding)
	}
	if st.LastIssues != 3 || st.LastComments != 5 || st.Cursor != "2026-07-10T00:00:00Z" || st.LastError != "" {
		t.Fatalf("result = %+v, want 3/5/cursor/no-error", st)
	}
}
