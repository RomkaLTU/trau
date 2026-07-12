package hubstore

import (
	"reflect"
	"strconv"
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
	if err := d.IngestTokens("repo", "COD-1", false, []TokenRow{{Seq: 10, Phase: "build"}}, 10); err != nil {
		t.Fatalf("IngestTokens: %v", err)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("EnsureDerivedSchema: %v", err)
	}
	calls, err := d.TokenCalls("repo", "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("token calls after re-ensure = %d, want 1 (no rebuild when version matches)", len(calls))
	}
}

func TestEnsureSchemaRebuildsOnVersionMismatch(t *testing.T) {
	stores, d := testDerived(t)

	// Authoritative state that a rebuild must not touch.
	if err := stores.Registrations().Register("/repo/root"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Derived state that a rebuild must drop.
	if err := d.IngestTokens("repo", "COD-1", false, []TokenRow{{Seq: 5, Phase: "build"}}, 5); err != nil {
		t.Fatalf("IngestTokens: %v", err)
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

	calls, err := d.TokenCalls("repo", "COD-1")
	if err != nil {
		t.Fatalf("TokenCalls: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("derived token calls survived rebuild = %d, want 0", len(calls))
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
	if _, err := d.db.Exec(`DROP TABLE token_calls`); err != nil {
		t.Fatalf("drop token_calls: %v", err)
	}
	if err := stores.EnsureDerivedSchema(); err != nil {
		t.Fatalf("EnsureDerivedSchema: %v", err)
	}
	if _, err := d.TokenCalls("repo", "COD-1"); err != nil {
		t.Fatalf("token_calls table not rebuilt: %v", err)
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

func TestCursorsZeroWhenAbsent(t *testing.T) {
	_, d := testDerived(t)
	if off, err := d.TokenCursor("nope", "x"); err != nil || off != 0 {
		t.Fatalf("TokenCursor(absent) = %d, %v; want 0, nil", off, err)
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
