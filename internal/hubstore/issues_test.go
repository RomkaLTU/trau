package hubstore

import (
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
