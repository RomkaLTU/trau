package webserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

func TestRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	cutoff, ok := retentionCutoff(now, 14)
	if !ok {
		t.Fatal("14 days should enable pruning")
	}
	if want := "2026-07-10T12:00:00Z"; cutoff != want {
		t.Errorf("cutoff = %q, want %q (14 days before now)", cutoff, want)
	}

	if _, ok := retentionCutoff(now, 0); ok {
		t.Error("0 days = keep forever, pruning must be disabled")
	}
	if _, ok := retentionCutoff(now, -3); ok {
		t.Error("a negative window must disable pruning")
	}
}

func TestTraceWithinRoot(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name  string
		trace string
		want  bool
	}{
		{"inside", filepath.Join(root, "session-1"), true},
		{"nested", filepath.Join(root, "a", "b"), true},
		{"the root itself", root, false},
		{"escape with dotdot", filepath.Join(root, "..", "elsewhere"), false},
		{"sibling prefix", root + "-other/session", false},
		{"unrelated absolute", "/tmp/somewhere/else", false},
		{"empty trace", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceWithinRoot(tc.trace, root); got != tc.want {
				t.Errorf("traceWithinRoot(%q, root) = %v, want %v", tc.trace, got, tc.want)
			}
		})
	}

	if traceWithinRoot(filepath.Join(root, "x"), "") {
		t.Error("an empty recordings root must refuse every path")
	}
}

func TestSweepProofsDeletesTraceClearsColumnAndEmits(t *testing.T) {
	home := t.TempDir()
	recordings := t.TempDir()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.proofRetention = 14
	s.recordingsRoot = recordings

	const repo, ticket = "/repos/acme", "COD-old"
	traceDir := filepath.Join(recordings, "session-old")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("make trace dir: %v", err)
	}

	shot, _, err := s.stores.Proofs().Blobs().Put(strings.NewReader("shot"), 0)
	if err != nil {
		t.Fatalf("put shot: %v", err)
	}
	backdated := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	if err := s.stores.Proofs().Replace(repo, ticket, []hubstore.RunProof{
		{Seq: 0, Kind: hubstore.ProofVideo, TraceDir: traceDir, CreatedAt: backdated},
		{Seq: 1, Kind: hubstore.ProofScreenshot, SHA256: shot, Mime: "image/png", TraceDir: traceDir, CreatedAt: backdated},
	}); err != nil {
		t.Fatalf("seed proofs: %v", err)
	}

	s.sweepProofs(context.Background())

	if _, err := os.Stat(traceDir); !os.IsNotExist(err) {
		t.Errorf("trace dir still present after sweep (err=%v), want it deleted", err)
	}
	rows, err := s.stores.Proofs().ForRun(repo, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("ForRun returned %d rows, want the hub-stored screenshot kept", len(rows))
	}
	for _, r := range rows {
		if r.TraceDir != "" {
			t.Errorf("row %d trace_dir = %q, want cleared", r.Seq, r.TraceDir)
		}
	}
	if rows[1].SHA256 != shot {
		t.Errorf("screenshot bytes = %q, want kept %q", rows[1].SHA256, shot)
	}

	evs, err := s.stores.Events().Recent(repo, 10, 0)
	if err != nil {
		t.Fatalf("Recent events: %v", err)
	}
	if !hasEventKind(evs, event.KindProofsSwept) {
		t.Errorf("no %s event emitted; got %+v", event.KindProofsSwept, evs)
	}
}

func TestSweepProofsSilentWhenNothingExpired(t *testing.T) {
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStores(t))
	s.proofRetention = 14
	s.recordingsRoot = t.TempDir()

	const repo, ticket = "/repos/acme", "COD-new"
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.stores.Proofs().Replace(repo, ticket, []hubstore.RunProof{
		{Seq: 0, Kind: hubstore.ProofVideo, TraceDir: "/rec/keep", CreatedAt: fresh},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.sweepProofs(context.Background())

	evs, err := s.stores.Events().Recent(repo, 10, 0)
	if err != nil {
		t.Fatalf("Recent events: %v", err)
	}
	if hasEventKind(evs, event.KindProofsSwept) {
		t.Error("a sweep with nothing to prune must stay silent")
	}
}

func TestSweepProofsRefusesTraceOutsideRoot(t *testing.T) {
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStores(t))
	s.proofRetention = 14
	s.recordingsRoot = t.TempDir()

	outside := filepath.Join(t.TempDir(), "not-a-recording")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("make dir: %v", err)
	}
	const repo, ticket = "/repos/acme", "COD-out"
	backdated := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	if err := s.stores.Proofs().Replace(repo, ticket, []hubstore.RunProof{
		{Seq: 0, Kind: hubstore.ProofVideo, TraceDir: outside, CreatedAt: backdated},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s.sweepProofs(context.Background())

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a trace outside the recordings root was deleted (err=%v), want it left intact", err)
	}
	rows, err := s.stores.Proofs().ForRun(repo, ticket)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(rows) != 1 || rows[0].TraceDir != outside {
		t.Errorf("rows = %+v, want the out-of-root trace column left untouched", rows)
	}
}

func hasEventKind(rows []hubstore.EventRow, kind string) bool {
	for _, r := range rows {
		if r.Kind == kind {
			return true
		}
	}
	return false
}
