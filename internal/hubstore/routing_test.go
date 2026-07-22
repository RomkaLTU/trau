package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testRouting(t *testing.T) *Routing {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewRouting(db.SQL())
}

func observe(t *testing.T, r *Routing, repo, hash string, keys map[string]string) ([]RoutingChange, string) {
	t.Helper()
	changes, prev, err := r.Observe(repo, RoutingFingerprint{Hash: hash, Keys: keys, TS: "2026-07-22T09:00:00Z"})
	if err != nil {
		t.Fatalf("observe %s: %v", hash, err)
	}
	return changes, prev
}

// TestRoutingObserveMarksBoundaries is the cohort contract: the first fingerprint
// a repo reports opens a cohort, re-reporting it changes nothing, and a routing key
// that moves reports exactly that key's before and after.
func TestRoutingObserveMarksBoundaries(t *testing.T) {
	r := testRouting(t)
	const repo = "/repos/acme"
	first := map[string]string{"PROVIDER": "claude", "PHASE_VERIFY": "claude:opus:xhigh"}

	changes, prev := observe(t, r, repo, "hash-1", first)
	if prev != "" {
		t.Errorf("previous hash = %q, want empty on the first observation", prev)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v, want every key reported on the first observation", changes)
	}

	if changes, _ := observe(t, r, repo, "hash-1", first); len(changes) != 0 {
		t.Fatalf("changes = %+v, want none for an unchanged config", changes)
	}

	second := map[string]string{"PROVIDER": "claude", "PHASE_VERIFY": "claude:opus:high"}
	changes, prev = observe(t, r, repo, "hash-2", second)
	if prev != "hash-1" {
		t.Errorf("previous hash = %q, want hash-1", prev)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want only the key that moved", changes)
	}
	want := RoutingChange{Key: "PHASE_VERIFY", From: "claude:opus:xhigh", To: "claude:opus:high"}
	if changes[0] != want {
		t.Errorf("change = %+v, want %+v", changes[0], want)
	}

	last, err := r.Last(repo)
	if err != nil {
		t.Fatalf("Last: %v", err)
	}
	if last.Hash != "hash-2" || last.Keys["PHASE_VERIFY"] != "claude:opus:high" {
		t.Errorf("last = %+v, want the newest fingerprint stored", last)
	}
}

// TestRoutingObserveReportsAddedAndDroppedKeys covers a fingerprint whose key set
// itself changed — a key gained or lost still has to name its side of the diff.
func TestRoutingObserveReportsAddedAndDroppedKeys(t *testing.T) {
	r := testRouting(t)
	const repo = "/repos/acme"
	observe(t, r, repo, "hash-1", map[string]string{"PROVIDER": "claude", "REQUIRED_SKILLS": "golang-pro"})

	changes, _ := observe(t, r, repo, "hash-2", map[string]string{"PROVIDER": "claude", "PHASE_BUILD": "claude:opus:high"})
	want := []RoutingChange{
		{Key: "PHASE_BUILD", From: "", To: "claude:opus:high"},
		{Key: "REQUIRED_SKILLS", From: "golang-pro", To: ""},
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	for i, c := range changes {
		if c != want[i] {
			t.Errorf("changes[%d] = %+v, want %+v", i, c, want[i])
		}
	}
}

// TestRoutingLastIsPerRepo keeps one repo's cohort boundary from moving another's.
func TestRoutingLastIsPerRepo(t *testing.T) {
	r := testRouting(t)
	observe(t, r, "/repos/acme", "hash-acme", map[string]string{"PROVIDER": "claude"})
	observe(t, r, "/repos/beta", "hash-beta", map[string]string{"PROVIDER": "codex"})

	acme, err := r.Last("/repos/acme")
	if err != nil {
		t.Fatalf("Last acme: %v", err)
	}
	if acme.Hash != "hash-acme" {
		t.Errorf("acme hash = %q, want hash-acme", acme.Hash)
	}
	unknown, err := r.Last("/repos/never-run")
	if err != nil {
		t.Fatalf("Last unknown: %v", err)
	}
	if unknown.Hash != "" {
		t.Errorf("unknown repo hash = %q, want empty", unknown.Hash)
	}
}
