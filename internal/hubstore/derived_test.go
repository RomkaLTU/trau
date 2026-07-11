package hubstore

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testDerived(t *testing.T) (*Stores, *Derived) {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stores := NewStores(db.SQL())
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("ensure derived schema: %v", err)
	}
	return stores, stores.Derived()
}

func floatPtr(v float64) *float64 { return &v }

func TestEnsureSchemaStampsVersion(t *testing.T) {
	stores, d := testDerived(t)

	var stored string
	if err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, derivedVersionKey).Scan(&stored); err != nil {
		t.Fatalf("read derived_version: %v", err)
	}
	if want := strconv.Itoa(derivedVersion); stored != want {
		t.Fatalf("derived_version = %q, want %q", stored, want)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("second EnsureDerivedSchema: %v", err)
	}
}

func TestEnsureSchemaPreservesDataWhenCurrent(t *testing.T) {
	stores, d := testDerived(t)
	if err := d.IngestEvents("repo", false, []EventRow{{Seq: 10, Kind: "phase"}}, 10); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("EnsureDerivedSchema: %v", err)
	}
	evs, err := d.Events("repo")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events after re-ensure = %d, want 1 (no rebuild when version matches)", len(evs))
	}
}

func TestEnsureSchemaRebuildsOnVersionMismatch(t *testing.T) {
	stores, d := testDerived(t)

	// Authoritative state that a rebuild must not touch.
	if err := stores.Registrations().Register("/repo/root"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Derived state that a rebuild must drop.
	if err := d.IngestEvents("repo", false, []EventRow{{Seq: 5, Kind: "phase"}}, 5); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}

	// Simulate a derived-schema version bump: the stored version no longer
	// matches the code's derivedVersion.
	if _, err := d.db.Exec(
		`UPDATE meta SET value = ? WHERE key = ?`, "999", derivedVersionKey,
	); err != nil {
		t.Fatalf("bump stored version: %v", err)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("EnsureDerivedSchema after bump: %v", err)
	}

	evs, err := d.Events("repo")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("derived events survived rebuild = %d, want 0", len(evs))
	}
	registered, err := stores.Registrations().Registered()
	if err != nil {
		t.Fatalf("Registered: %v", err)
	}
	if !reflect.DeepEqual(registered, []string{"/repo/root"}) {
		t.Fatalf("authoritative registrations = %v, want [/repo/root]", registered)
	}
	var stored string
	if err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, derivedVersionKey).Scan(&stored); err != nil {
		t.Fatalf("read derived_version: %v", err)
	}
	if want := strconv.Itoa(derivedVersion); stored != want {
		t.Fatalf("derived_version after rebuild = %q, want %q", stored, want)
	}
}

func TestEnsureSchemaRebuildsOnMissingTable(t *testing.T) {
	stores, d := testDerived(t)
	if _, err := d.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("drop events: %v", err)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("EnsureDerivedSchema: %v", err)
	}
	if _, err := d.Events("repo"); err != nil {
		t.Fatalf("events table not rebuilt: %v", err)
	}
}

func TestRecentEventsLimitAndCursor(t *testing.T) {
	_, d := testDerived(t)
	rows := make([]EventRow, 5)
	for i := range rows {
		rows[i] = EventRow{Seq: int64((i + 1) * 10), Kind: "phase", Msg: strconv.Itoa(i + 1)}
	}
	if err := d.IngestEvents("repo", false, rows, 50); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}

	page, err := d.RecentEvents("repo", 2, 0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if got := seqLine(page); got != "5040" {
		t.Fatalf("latest page seqs = %q, want 5040 (newest first)", got)
	}

	older, err := d.RecentEvents("repo", 2, page[len(page)-1].Seq)
	if err != nil {
		t.Fatalf("RecentEvents older: %v", err)
	}
	if got := seqLine(older); got != "3020" {
		t.Fatalf("older page seqs = %q, want 3020", got)
	}

	last, err := d.RecentEvents("repo", 2, older[len(older)-1].Seq)
	if err != nil {
		t.Fatalf("RecentEvents last: %v", err)
	}
	if got := seqLine(last); got != "10" {
		t.Fatalf("last page seqs = %q, want 10", got)
	}
}

func seqLine(rows []EventRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strconv.FormatInt(r.Seq, 10))
	}
	return b.String()
}

func TestRecentEventsEmptyRepo(t *testing.T) {
	_, d := testDerived(t)
	got, err := d.RecentEvents("repo", 10, 0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recent events = %d, want 0", len(got))
	}
}

func TestIngestEventsAppendThenResync(t *testing.T) {
	_, d := testDerived(t)

	first := []EventRow{
		{Seq: 20, TS: "t1", Kind: "phase_start", Phase: "build", Msg: "go", Fields: `{"a":1}`},
		{Seq: 40, TS: "t2", Kind: "phase_end", Phase: "build"},
	}
	if err := d.IngestEvents("repo", false, first, 40); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	if off, err := d.EventCursor("repo"); err != nil || off != 40 {
		t.Fatalf("EventCursor = %d, %v; want 40, nil", off, err)
	}
	got, err := d.Events("repo")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("events = %+v, want %+v", got, first)
	}

	// A rewrite shorter than the cursor resyncs: old rows dropped, new set kept.
	rewritten := []EventRow{{Seq: 15, TS: "t3", Kind: "phase_start", Phase: "verify"}}
	if err := d.IngestEvents("repo", true, rewritten, 15); err != nil {
		t.Fatalf("IngestEvents resync: %v", err)
	}
	got, err = d.Events("repo")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if !reflect.DeepEqual(got, rewritten) {
		t.Fatalf("events after resync = %+v, want %+v", got, rewritten)
	}
}

func TestIngestTokensRoundTrip(t *testing.T) {
	_, d := testDerived(t)
	rows := []TokenRow{
		{Seq: 30, TS: "t1", Phase: "build", Input: 10, Output: 20, Total: 30, CostUSD: floatPtr(0.5), Turns: 2, Provider: "claude", Model: "opus", Context: 1000, Skills: `["golang-pro"]`},
		{Seq: 60, TS: "t2", Phase: "verify", Input: 5, Output: 5, Total: 10, CostUSD: nil, IsError: true},
	}
	if err := d.IngestTokens("repo", "COD-1", false, rows, 60); err != nil {
		t.Fatalf("IngestTokens: %v", err)
	}
	if off, err := d.TokenCursor("repo", "COD-1"); err != nil || off != 60 {
		t.Fatalf("TokenCursor = %d, %v; want 60, nil", off, err)
	}
	got, err := d.TokenCalls("repo", "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("token calls = %+v, want %+v", got, rows)
	}
}

func TestUpsertCheckpoint(t *testing.T) {
	_, d := testDerived(t)
	cp := CheckpointRow{Phase: "built", Title: "Do it", Branch: "feature/x", UpdatedAt: "2026-07-11 10:00:00", Data: `{"PHASE":"built"}`}
	if err := d.UpsertCheckpoint("repo", "COD-1", cp, 128, 42); err != nil {
		t.Fatalf("UpsertCheckpoint: %v", err)
	}
	size, mtime, err := d.CheckpointCursor("repo", "COD-1")
	if err != nil || size != 128 || mtime != 42 {
		t.Fatalf("CheckpointCursor = %d, %d, %v; want 128, 42, nil", size, mtime, err)
	}
	got, ok, err := d.Checkpoint("repo", "COD-1")
	if err != nil || !ok {
		t.Fatalf("Checkpoint ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, cp) {
		t.Fatalf("checkpoint = %+v, want %+v", got, cp)
	}

	next := CheckpointRow{Phase: "merged", Title: "Do it", Data: `{"PHASE":"merged"}`}
	if err := d.UpsertCheckpoint("repo", "COD-1", next, 200, 99); err != nil {
		t.Fatalf("UpsertCheckpoint update: %v", err)
	}
	got, _, err = d.Checkpoint("repo", "COD-1")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if got.Phase != "merged" {
		t.Fatalf("checkpoint phase after update = %q, want merged", got.Phase)
	}
}

func TestCursorsZeroWhenAbsent(t *testing.T) {
	_, d := testDerived(t)
	if off, err := d.EventCursor("nope"); err != nil || off != 0 {
		t.Fatalf("EventCursor(absent) = %d, %v; want 0, nil", off, err)
	}
	size, mtime, err := d.CheckpointCursor("nope", "x")
	if err != nil || size != 0 || mtime != 0 {
		t.Fatalf("CheckpointCursor(absent) = %d, %d, %v; want 0, 0, nil", size, mtime, err)
	}
	if _, ok, err := d.Checkpoint("nope", "x"); ok || err != nil {
		t.Fatalf("Checkpoint(absent) ok=%v err=%v; want false, nil", ok, err)
	}
}

func TestCostCellsAggregatesWindow(t *testing.T) {
	_, d := testDerived(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Two tickets in repoA fold into one (day, provider, model, phase) cell; a
	// nil-cost call reads as unmetered; repoB's June call falls outside the window.
	must(d.IngestTokens("repoA", "COD-1", false, []TokenRow{
		{Seq: 10, TS: "2026-07-10T09:00:00", Phase: "build", Total: 100, CostUSD: floatPtr(0.5), Provider: "claude", Model: "opus"},
		{Seq: 20, TS: "2026-07-10T10:00:00", Phase: "build", Total: 50, CostUSD: floatPtr(0.25), Provider: "claude", Model: "opus"},
		{Seq: 30, TS: "2026-07-11T09:00:00", Phase: "verify", Total: 25, CostUSD: nil, Provider: "claude", Model: "opus"},
	}, 30))
	must(d.IngestTokens("repoA", "COD-9", false, []TokenRow{
		{Seq: 10, TS: "2026-07-10T11:00:00", Phase: "build", Total: 10, CostUSD: floatPtr(0.125), Provider: "claude", Model: "opus"},
	}, 10))
	must(d.IngestTokens("repoB", "COD-2", false, []TokenRow{
		{Seq: 10, TS: "2026-07-11T09:00:00", Phase: "build", Total: 200, CostUSD: floatPtr(0.5), Provider: "codex", Model: "gpt"},
		{Seq: 20, TS: "2026-06-01T09:00:00", Phase: "build", Total: 999, CostUSD: floatPtr(9.99), Provider: "codex", Model: "gpt"},
	}, 20))

	cells, err := d.CostCells("2026-07-10", "2026-07-11")
	if err != nil {
		t.Fatalf("CostCells: %v", err)
	}
	want := []CostCell{
		{Repo: "repoA", Date: "2026-07-10", Phase: "build", Provider: "claude", Model: "opus", Tokens: 160, Cost: 0.875, Metered: true},
		{Repo: "repoA", Date: "2026-07-11", Phase: "verify", Provider: "claude", Model: "opus", Tokens: 25, Cost: 0, Metered: false},
		{Repo: "repoB", Date: "2026-07-11", Phase: "build", Provider: "codex", Model: "gpt", Tokens: 200, Cost: 0.5, Metered: true},
	}
	if !reflect.DeepEqual(cells, want) {
		t.Fatalf("cells = %+v, want %+v", cells, want)
	}
}

func TestCheckpointsListsRepoByTicket(t *testing.T) {
	_, d := testDerived(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.UpsertCheckpoint("repoA", "COD-2", CheckpointRow{Phase: "built", Data: `{"PHASE":"built"}`}, 1, 1))
	must(d.UpsertCheckpoint("repoA", "COD-1", CheckpointRow{Phase: "merged", Title: "First", Data: `{"PHASE":"merged"}`}, 1, 1))
	must(d.UpsertCheckpoint("repoB", "COD-3", CheckpointRow{Phase: "verified", Data: `{}`}, 1, 1))

	got, err := d.Checkpoints("repoA")
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	want := []TicketCheckpoint{
		{Ticket: "COD-1", CheckpointRow: CheckpointRow{Phase: "merged", Title: "First", Data: `{"PHASE":"merged"}`}},
		{Ticket: "COD-2", CheckpointRow: CheckpointRow{Phase: "built", Data: `{"PHASE":"built"}`}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checkpoints = %+v, want %+v", got, want)
	}
}
