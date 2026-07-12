package hubstore

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/transcriptdb"
)

func newTranscripts(t *testing.T, retention int) *Transcripts {
	t.Helper()
	tdb, err := transcriptdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open transcript db: %v", err)
	}
	t.Cleanup(func() { _ = tdb.Close() })
	return NewTranscripts(tdb.SQL(), retention)
}

func appendChunks(t *testing.T, ts *Transcripts, repo string, chunks ...NewTranscriptChunk) {
	t.Helper()
	if err := ts.Append(repo, chunks); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestTranscriptsAppendAndRead checks chunks round-trip through the store, appends
// are idempotent, and the readers page by seq and report the session's dimensions.
func TestTranscriptsAppendAndRead(t *testing.T) {
	ts := newTranscripts(t, 50)
	appendChunks(t, ts, "acme",
		NewTranscriptChunk{Stem: "100-build", Seq: 0, Cols: 80, Rows: 24, Data: []byte("AAA")},
		NewTranscriptChunk{Stem: "100-build", Seq: 1, Cols: 80, Rows: 24, Data: []byte("BBB")},
	)
	// A retried batch re-sending seq 0 must not duplicate the row.
	appendChunks(t, ts, "acme", NewTranscriptChunk{Stem: "100-build", Seq: 0, Cols: 80, Rows: 24, Data: []byte("AAA")})

	sessions, err := ts.Sessions("acme")
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Stem != "100-build" || sessions[0].Size != 6 || sessions[0].Cols != 80 {
		t.Fatalf("sessions = %+v, want one 6-byte build session at 80 cols", sessions)
	}

	all, err := ts.Chunks("acme", "100-build", -1)
	if err != nil {
		t.Fatalf("chunks: %v", err)
	}
	if len(all) != 2 || string(all[0].Data) != "AAA" || string(all[1].Data) != "BBB" {
		t.Fatalf("chunks from top = %+v, want AAA then BBB", all)
	}
	tail, err := ts.Chunks("acme", "100-build", 0)
	if err != nil {
		t.Fatalf("chunks tail: %v", err)
	}
	if len(tail) != 1 || string(tail[0].Data) != "BBB" {
		t.Fatalf("chunks after seq 0 = %+v, want only BBB", tail)
	}

	if cols, rows, ok, err := ts.Dims("acme", "100-build"); err != nil || !ok || cols != 80 || rows != 24 {
		t.Fatalf("dims = %d x %d ok=%v err=%v, want 80x24", cols, rows, ok, err)
	}
}

// TestTranscriptsNewestStem checks follow-mode resolution picks the most recent
// session and honors the since bound.
func TestTranscriptsNewestStem(t *testing.T) {
	ts := newTranscripts(t, 50)
	appendChunks(t, ts, "acme", NewTranscriptChunk{Stem: "100-build", Seq: 0, Cols: 80, Rows: 24, Data: []byte("x")})
	appendChunks(t, ts, "acme", NewTranscriptChunk{Stem: "300-verify", Seq: 0, Cols: 100, Rows: 40, Data: []byte("y")})

	if stem, _, _, ok, _ := ts.NewestStem("acme", 0); !ok || stem != "300-verify" {
		t.Errorf("newest = %q ok=%v, want 300-verify", stem, ok)
	}
	if stem, _, _, ok, _ := ts.NewestStem("acme", 200); !ok || stem != "300-verify" {
		t.Errorf("newest since 200 = %q ok=%v, want 300-verify", stem, ok)
	}
	if _, _, _, ok, _ := ts.NewestStem("acme", 400); ok {
		t.Error("newest since 400 must resolve nothing")
	}
}

// TestTranscriptsPruneKeepsNewest checks retention keeps the most recent sessions
// per repo and drops older ones, scoped per repo.
func TestTranscriptsPruneKeepsNewest(t *testing.T) {
	ts := newTranscripts(t, 2)
	for _, stem := range []string{"100-a", "200-b", "300-c"} {
		appendChunks(t, ts, "acme", NewTranscriptChunk{Stem: stem, Seq: 0, Cols: 80, Rows: 24, Data: []byte("x")})
	}
	appendChunks(t, ts, "other", NewTranscriptChunk{Stem: "50-z", Seq: 0, Cols: 80, Rows: 24, Data: []byte("x")})

	if err := ts.Prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}

	sessions, _ := ts.Sessions("acme")
	if len(sessions) != 2 || sessions[0].Stem != "300-c" || sessions[1].Stem != "200-b" {
		t.Fatalf("after prune = %+v, want the two newest (300-c, 200-b)", sessions)
	}
	if other, _ := ts.Sessions("other"); len(other) != 1 {
		t.Errorf("other repo pruned to %d, want its one session kept", len(other))
	}
}
