package hubstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// seedQueryEvents appends a small incident-shaped feed and returns the events store.
func seedQueryEvents(t *testing.T) *Events {
	t.Helper()
	e := testEvents(t)
	fields := func(m map[string]any) string {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal fields: %v", err)
		}
		return string(b)
	}
	_, err := e.Append("acme", []NewEvent{
		{TS: "2026-07-12T10:00:00Z", Kind: "agent_start", Phase: "build", Msg: "start"},
		{TS: "2026-07-12T10:01:00Z", Kind: "state_change", Phase: "build", Fields: fields(map[string]any{"ticket": "COD-1", "state": "faulted", "reason": "boom"})},
		{TS: "2026-07-12T10:02:00Z", Kind: "agent_call", Phase: "verify", Msg: "COD-1 verify call"},
		{TS: "2026-07-12T10:03:00Z", Kind: "cost_anomaly", Msg: "COD-2: cost anomaly", Fields: fields(map[string]any{"id": "COD-2"})},
		{TS: "2026-07-12T10:04:00Z", Kind: "state_change", Phase: "pr_open", Fields: fields(map[string]any{"ticket": "COD-2", "state": "merged"})},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return e
}

func msgsAndKinds(rows []EventRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Kind
	}
	return out
}

func TestEventsQuery(t *testing.T) {
	e := seedQueryEvents(t)

	t.Run("no filter is chronological", func(t *testing.T) {
		rows, err := e.Query("acme", EventFilter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) != 5 {
			t.Fatalf("rows = %d, want 5", len(rows))
		}
		for i := 1; i < len(rows); i++ {
			if rows[i-1].ID >= rows[i].ID {
				t.Fatalf("not chronological at %d: %d then %d", i, rows[i-1].ID, rows[i].ID)
			}
		}
	})

	t.Run("kind filter", func(t *testing.T) {
		rows, err := e.Query("acme", EventFilter{Kind: "state_change"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if got := msgsAndKinds(rows); len(got) != 2 || got[0] != "state_change" || got[1] != "state_change" {
			t.Fatalf("kinds = %v, want two state_change", got)
		}
	})

	t.Run("ticket matches fields and msg", func(t *testing.T) {
		rows, err := e.Query("acme", EventFilter{Ticket: "COD-1"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		// The state_change carries COD-1 in fields.ticket; the agent_call mentions it in msg.
		if got := msgsAndKinds(rows); len(got) != 2 {
			t.Fatalf("COD-1 events = %v, want state_change + agent_call", got)
		}

		rows, err = e.Query("acme", EventFilter{Ticket: "COD-2"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		// cost_anomaly carries COD-2 in fields.id; the merge state_change in fields.ticket.
		if len(rows) != 2 {
			t.Fatalf("COD-2 events = %d, want 2", len(rows))
		}
	})

	t.Run("ticket does not prefix-bleed into longer ids", func(t *testing.T) {
		b := testEvents(t)
		if _, err := b.Append("acme", []NewEvent{
			{Kind: "state_change", Fields: `{"ticket":"COD-1"}`},
			{Kind: "state_change", Fields: `{"ticket":"COD-10"}`},
			{Kind: "agent_call", Msg: "COD-10 retry"},
			{Kind: "cost_anomaly", Fields: `{"id":"COD-11"}`},
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		rows, err := b.Query("acme", EventFilter{Ticket: "COD-1"})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) != 1 || rows[0].Fields != `{"ticket":"COD-1"}` {
			t.Fatalf("COD-1 query bled into longer ids: %v", rows)
		}
	})

	t.Run("grep is case-insensitive over payload", func(t *testing.T) {
		for _, pat := range []string{"faulted", "FAULTED", "boom"} {
			rows, err := e.Query("acme", EventFilter{Grep: pat})
			if err != nil {
				t.Fatalf("Query %q: %v", pat, err)
			}
			if len(rows) != 1 || rows[0].Kind != "state_change" {
				t.Fatalf("grep %q = %v, want the faulted state_change", pat, msgsAndKinds(rows))
			}
		}
	})

	t.Run("since bounds the window", func(t *testing.T) {
		since, _ := time.Parse(time.RFC3339, "2026-07-12T10:03:00Z")
		rows, err := e.Query("acme", EventFilter{Since: since})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("since rows = %d, want 2 (the two at/after 10:03)", len(rows))
		}
		if rows[0].Kind != "cost_anomaly" {
			t.Fatalf("first since row = %q, want cost_anomaly", rows[0].Kind)
		}
	})

	t.Run("after pages forward", func(t *testing.T) {
		all, err := e.Query("acme", EventFilter{})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		third := all[2].ID
		rows, err := e.Query("acme", EventFilter{After: third})
		if err != nil {
			t.Fatalf("Query after: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("after rows = %d, want 2", len(rows))
		}
		for _, r := range rows {
			if r.ID <= third {
				t.Fatalf("row id %d not past cursor %d", r.ID, third)
			}
		}
	})

	t.Run("limit keeps the newest", func(t *testing.T) {
		rows, err := e.Query("acme", EventFilter{Limit: 2})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(rows))
		}
		// Newest two, returned oldest first.
		if rows[0].Kind != "cost_anomaly" || rows[1].Kind != "state_change" {
			t.Fatalf("limited window = %v, want the newest two chronological", msgsAndKinds(rows))
		}
	})
}
