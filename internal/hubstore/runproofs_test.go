package hubstore

import (
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testRunProofs(t *testing.T) *RunProofs {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{}).Proofs()
}

func putProof(t *testing.T, p *RunProofs, content string) string {
	t.Helper()
	sha, _, err := p.Blobs().Put(strings.NewReader(content), 0)
	if err != nil {
		t.Fatalf("Put %q: %v", content, err)
	}
	return sha
}

func TestRunProofsReplaceRoundTrip(t *testing.T) {
	p := testRunProofs(t)
	const repo, ticket = "/repos/acme", "COD-1"

	sha := putProof(t, p, "shot-one")
	proofs := []RunProof{
		{Seq: 0, Kind: ProofVideo, TraceDir: "/tmp/rec/abc", CreatedAt: "t0"},
		{Seq: 1, Kind: ProofScreenshot, SHA256: sha, Mime: "image/png", Caption: "login", TraceDir: "/tmp/rec/abc", CreatedAt: "t0"},
	}
	if err := p.Replace(repo, ticket, proofs); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := p.ForRun(repo, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ForRun returned %d rows, want 2", len(got))
	}
	if got[0].Kind != ProofVideo || got[0].TraceDir != "/tmp/rec/abc" {
		t.Errorf("row 0 = %+v, want the video/trace row first", got[0])
	}
	if got[1].Kind != ProofScreenshot || got[1].SHA256 != sha || got[1].Caption != "login" {
		t.Errorf("row 1 = %+v, want the login screenshot", got[1])
	}

	one, found, err := p.Find(repo, ticket, 1)
	if err != nil || !found {
		t.Fatalf("Find seq 1: found=%v err=%v", found, err)
	}
	if one.SHA256 != sha {
		t.Errorf("Find seq 1 sha = %q, want %q", one.SHA256, sha)
	}
}

func TestRunProofsReplaceSupersedesPrior(t *testing.T) {
	p := testRunProofs(t)
	const repo, ticket = "/repos/acme", "COD-2"

	first := putProof(t, p, "attempt-1")
	if err := p.Replace(repo, ticket, []RunProof{
		{Seq: 1, Kind: ProofScreenshot, SHA256: first, Mime: "image/png"},
		{Seq: 2, Kind: ProofScreenshot, SHA256: first, Mime: "image/png"},
	}); err != nil {
		t.Fatalf("first Replace: %v", err)
	}

	second := putProof(t, p, "attempt-2")
	if err := p.Replace(repo, ticket, []RunProof{
		{Seq: 1, Kind: ProofScreenshot, SHA256: second, Mime: "image/png", Caption: "retry"},
	}); err != nil {
		t.Fatalf("second Replace: %v", err)
	}

	got, err := p.ForRun(repo, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ForRun returned %d rows, want the retry attempt to have replaced the prior rows", len(got))
	}
	if got[0].SHA256 != second || got[0].Caption != "retry" {
		t.Errorf("row = %+v, want only the retry screenshot", got[0])
	}
}

func TestRunProofsReplaceEmptyClears(t *testing.T) {
	p := testRunProofs(t)
	const repo, ticket = "/repos/acme", "COD-3"

	if err := p.Replace(repo, ticket, []RunProof{{Seq: 1, Kind: ProofScreenshot, SHA256: putProof(t, p, "x")}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := p.Replace(repo, ticket, nil); err != nil {
		t.Fatalf("Replace empty: %v", err)
	}
	got, err := p.ForRun(repo, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ForRun returned %d rows, want the run cleared", len(got))
	}
}
