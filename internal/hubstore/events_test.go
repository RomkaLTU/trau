package hubstore

import (
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testEvents(t *testing.T) *Events {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewEvents(db.SQL(), 0)
}

func appendKinds(t *testing.T, e *Events, repo string, kinds ...string) []EventRow {
	t.Helper()
	evs := make([]NewEvent, len(kinds))
	for i, k := range kinds {
		evs[i] = NewEvent{Kind: k, Msg: k}
	}
	rows, err := e.Append(repo, evs)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return rows
}

func msgLine(rows []EventRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.Msg)
	}
	return b.String()
}

func TestEventsAppendAssignsMonotonicIDs(t *testing.T) {
	e := testEvents(t)
	rows := appendKinds(t, e, "repo", "a", "b", "c")
	if len(rows) != 3 {
		t.Fatalf("appended rows = %d, want 3", len(rows))
	}
	if rows[0].ID >= rows[1].ID || rows[1].ID >= rows[2].ID {
		t.Fatalf("ids not monotonic: %d, %d, %d", rows[0].ID, rows[1].ID, rows[2].ID)
	}
	// A second batch keeps climbing from where the first left off.
	more := appendKinds(t, e, "repo", "d")
	if more[0].ID <= rows[2].ID {
		t.Fatalf("second batch id %d not above first batch %d", more[0].ID, rows[2].ID)
	}
}

func TestEventsRecentLimitAndCursor(t *testing.T) {
	e := testEvents(t)
	appendKinds(t, e, "repo", "1", "2", "3", "4", "5")

	page, err := e.Recent("repo", 2, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	// Recent returns newest first; the older page is bounded below the last id.
	if got := msgLine(page); got != "54" {
		t.Fatalf("latest page = %q, want 54 (newest first)", got)
	}
	older, err := e.Recent("repo", 2, page[len(page)-1].ID)
	if err != nil {
		t.Fatalf("Recent older: %v", err)
	}
	if got := msgLine(older); got != "32" {
		t.Fatalf("older page = %q, want 32", got)
	}
	last, err := e.Recent("repo", 2, older[len(older)-1].ID)
	if err != nil {
		t.Fatalf("Recent last: %v", err)
	}
	if got := msgLine(last); got != "1" {
		t.Fatalf("last page = %q, want 1", got)
	}
}

func TestEventsSince(t *testing.T) {
	e := testEvents(t)
	rows := appendKinds(t, e, "repo", "1", "2", "3", "4")
	got, err := e.Since("repo", rows[1].ID)
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if line := msgLine(got); line != "34" {
		t.Fatalf("Since = %q, want 34", line)
	}
	// A cursor at the tip yields nothing.
	tail, err := e.Since("repo", rows[3].ID)
	if err != nil {
		t.Fatalf("Since tip: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("Since tip = %d rows, want 0", len(tail))
	}
}

func TestEventsRepoScoped(t *testing.T) {
	e := testEvents(t)
	appendKinds(t, e, "repoA", "a1", "a2")
	appendKinds(t, e, "repoB", "b1")
	a, err := e.Recent("repoA", 10, 0)
	if err != nil {
		t.Fatalf("Recent repoA: %v", err)
	}
	// Newest first, and repoB's event is excluded.
	if got := msgLine(a); got != "a2a1" {
		t.Fatalf("repoA events = %q, want a2a1", got)
	}
}

func TestEventsAllStreams(t *testing.T) {
	e := testEvents(t)
	appendKinds(t, e, "repoA", "a1")
	appendKinds(t, e, "repoB", "b1")
	appendKinds(t, e, "repoA", "a2")

	recent, err := e.RecentAll(10, 0)
	if err != nil {
		t.Fatalf("RecentAll: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("RecentAll = %d rows, want 3", len(recent))
	}
	// Newest first, interleaved across repos by global id.
	if recent[0].Msg != "a2" || recent[0].Repo != "repoA" {
		t.Fatalf("RecentAll newest = %+v, want a2/repoA", recent[0])
	}

	since, err := e.SinceAll(recent[1].ID)
	if err != nil {
		t.Fatalf("SinceAll: %v", err)
	}
	if len(since) != 1 || since[0].Msg != "a2" {
		t.Fatalf("SinceAll = %+v, want just a2", since)
	}
}

func TestEventsHasKind(t *testing.T) {
	e := testEvents(t)
	if _, err := e.Append("repo", []NewEvent{
		{Kind: "build_no_skills", Fields: `{"ticket":"COD-1"}`},
		{Kind: "state_change", Fields: `{"ticket":"COD-2"}`},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	has, err := e.HasKind("repo", "build_no_skills", "COD-1")
	if err != nil {
		t.Fatalf("HasKind: %v", err)
	}
	if !has {
		t.Fatal("HasKind(build_no_skills, COD-1) = false, want true")
	}
	// The kind exists but not for this ticket.
	has, err = e.HasKind("repo", "build_no_skills", "COD-2")
	if err != nil {
		t.Fatalf("HasKind: %v", err)
	}
	if has {
		t.Fatal("HasKind(build_no_skills, COD-2) = true, want false")
	}
	// Wrong repo.
	has, err = e.HasKind("other", "build_no_skills", "COD-1")
	if err != nil {
		t.Fatalf("HasKind: %v", err)
	}
	if has {
		t.Fatal("HasKind(other repo) = true, want false")
	}
}

func TestEventsAppendEmpty(t *testing.T) {
	e := testEvents(t)
	rows, err := e.Append("repo", nil)
	if err != nil {
		t.Fatalf("Append(nil): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("Append(nil) = %d rows, want 0", len(rows))
	}
}

func TestEventsPruneKeepsRecentPerRepo(t *testing.T) {
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := NewEvents(db.SQL(), 3)

	appendKinds(t, e, "a", "1", "2", "3", "4", "5")
	appendKinds(t, e, "b", "x", "y")

	if err := e.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	rows, err := e.Recent("a", 10, 0)
	if err != nil {
		t.Fatalf("Recent a: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("repo a events after prune = %d, want 3", len(rows))
	}
	if rows[0].Kind != "5" || rows[2].Kind != "3" {
		t.Fatalf("repo a kept wrong window: newest %q oldest %q, want 5..3", rows[0].Kind, rows[2].Kind)
	}
	if under, _ := e.Recent("b", 10, 0); len(under) != 2 {
		t.Fatalf("repo b under the window pruned: got %d, want 2", len(under))
	}
}

func TestEventsPruneDisabled(t *testing.T) {
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := NewEvents(db.SQL(), 0)
	appendKinds(t, e, "a", "1", "2", "3", "4", "5")
	if err := e.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if rows, _ := e.Recent("a", 10, 0); len(rows) != 5 {
		t.Fatalf("disabled retention pruned rows: got %d, want 5", len(rows))
	}
}
