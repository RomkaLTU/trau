package hubstore

import (
	"slices"
	"testing"
)

func searchIDs(t *testing.T, s *Issues, repo, query string) []string {
	t.Helper()
	got, err := s.Search(repo, query, 0)
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	ids := make([]string, len(got))
	for i, iss := range got {
		ids[i] = iss.Identifier
	}
	return ids
}

func TestSearchMatchesTitleAndDescription(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "Full-text issue search", Description: "rank matches over FTS5"},
		{Identifier: "COD-2", Title: "Queue draining", Description: "settle epics when the queue empties"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if got := searchIDs(t, s, "/repo/acme", "search"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("title-word search = %v, want [COD-1]", got)
	}
	if got := searchIDs(t, s, "/repo/acme", "settle"); !slices.Equal(got, []string{"COD-2"}) {
		t.Fatalf("description-word search = %v, want [COD-2]", got)
	}
}

func TestSearchCoversInternalAndSynced(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "internal", []Issue{
		{Identifier: "ACME-1", Title: "authentication rework"},
	}); err != nil {
		t.Fatalf("Upsert internal: %v", err)
	}
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-9", Title: "authentication regression"},
	}); err != nil {
		t.Fatalf("Upsert synced: %v", err)
	}

	got := searchIDs(t, s, "/repo/acme", "authentication")
	if len(got) != 2 || !slices.Contains(got, "ACME-1") || !slices.Contains(got, "COD-9") {
		t.Fatalf("search = %v, want both the internal and synced issue", got)
	}
}

func TestSearchRanksTitleAboveDescription(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "unrelated", Description: "a passing mention of telemetry"},
		{Identifier: "COD-2", Title: "telemetry dashboard", Description: "unrelated"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got := searchIDs(t, s, "/repo/acme", "telemetry")
	if len(got) != 2 || got[0] != "COD-2" {
		t.Fatalf("ranking = %v, want the title match COD-2 first", got)
	}
}

func TestSearchStaysConsistentThroughEdits(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "alpha widget"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := searchIDs(t, s, "/repo/acme", "alpha"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("pre-edit search = %v, want [COD-1]", got)
	}

	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "beta gadget"},
	}); err != nil {
		t.Fatalf("Upsert edit: %v", err)
	}

	if got := searchIDs(t, s, "/repo/acme", "alpha"); len(got) != 0 {
		t.Fatalf("stale term search = %v, want none after the edit", got)
	}
	if got := searchIDs(t, s, "/repo/acme", "gadget"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("new term search = %v, want [COD-1] with no manual rebuild", got)
	}
}

func TestSearchIsScopedByRepo(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/a", "linear", []Issue{{Identifier: "A-1", Title: "shared keyword"}}); err != nil {
		t.Fatalf("Upsert a: %v", err)
	}
	if _, _, err := s.Upsert("/repo/b", "linear", []Issue{{Identifier: "B-1", Title: "shared keyword"}}); err != nil {
		t.Fatalf("Upsert b: %v", err)
	}

	if got := searchIDs(t, s, "/repo/a", "shared"); !slices.Equal(got, []string{"A-1"}) {
		t.Fatalf("repo-a search = %v, want only A-1", got)
	}
	if got := searchIDs(t, s, "/repo/b", "shared"); !slices.Equal(got, []string{"B-1"}) {
		t.Fatalf("repo-b search = %v, want only B-1", got)
	}
}

func TestSearchLabelsAreIndexed(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "nothing special", Labels: []string{"regression", "p1"}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := searchIDs(t, s, "/repo/acme", "regression"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("label search = %v, want [COD-1]", got)
	}
}

func TestSearchMatchesAssigneeName(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "nothing special", AssigneeID: "u-1", AssigneeName: "Ada Lovelace"},
		{Identifier: "COD-2", Title: "unrelated", AssigneeID: "u-2", AssigneeName: "Bob Martin"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := searchIDs(t, s, "/repo/acme", "lovelace"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("assignee search = %v, want [COD-1]", got)
	}
	if got := searchIDs(t, s, "/repo/acme", "ada"); !slices.Equal(got, []string{"COD-1"}) {
		t.Fatalf("first-name search = %v, want [COD-1]", got)
	}
}

func TestSearchHandlesSpecialCharactersAndEmptyResults(t *testing.T) {
	s := testIssues(t)
	if _, _, err := s.Upsert("/repo/acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "widget", Description: "hello world"},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"ticket-style id", "COD-1", []string{"COD-1"}},
		{"quoted term", `"widget"`, []string{"COD-1"}},
		{"prefix", "wid", []string{"COD-1"}},
		{"blank", "", nil},
		{"whitespace", "   ", nil},
		{"punctuation only", `-()*:"`, nil},
		{"no matches", "nomatchxyz", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Search("/repo/acme", tt.query, 0)
			if err != nil {
				t.Fatalf("Search(%q): %v", tt.query, err)
			}
			ids := make([]string, len(got))
			for i, iss := range got {
				ids[i] = iss.Identifier
			}
			if !slices.Equal(ids, tt.want) {
				t.Fatalf("Search(%q) = %v, want %v", tt.query, ids, tt.want)
			}
		})
	}
}

func TestSearchLimitClamps(t *testing.T) {
	s := testIssues(t)
	issues := make([]Issue, 0, defaultSearchLimit+5)
	for i := range defaultSearchLimit + 5 {
		issues = append(issues, Issue{Identifier: "COD-" + string(rune('a'+i)), Title: "common term"})
	}
	if _, _, err := s.Upsert("/repo/acme", "linear", issues); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Search("/repo/acme", "common", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != defaultSearchLimit {
		t.Fatalf("results = %d, want the default limit of %d", len(got), defaultSearchLimit)
	}
}
