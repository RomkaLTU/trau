package hubstore

import (
	"errors"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

var steerEpoch = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func testSteerNotes(t *testing.T) *SteerNotes {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewSteerNotes(db.SQL())
	s.now = func() time.Time { return steerEpoch }
	return s
}

func queueSteerNote(t *testing.T, s *SteerNotes, repo, ticket, body string) SteerNote {
	t.Helper()
	note, err := s.Queue(repo, ticket, body)
	if err != nil {
		t.Fatalf("Queue(%q, %q): %v", repo, ticket, err)
	}
	return note
}

func TestSteerNoteQueueListsOldestFirst(t *testing.T) {
	s := testSteerNotes(t)
	first := queueSteerNote(t, s, "/repo", "COD-1", "check the migration")
	second := queueSteerNote(t, s, "/repo", "COD-1", "and the\nsecond line")

	if first.ID >= second.ID {
		t.Fatalf("ids are not ascending: first=%d second=%d", first.ID, second.ID)
	}
	if first.Status != SteerPending || first.DeliveredPhase != "" || first.DeliveredAt != "" {
		t.Fatalf("queued note = %+v, want pending and unstamped", first)
	}
	if first.CreatedAt != steerEpoch.Format(time.RFC3339) {
		t.Fatalf("CreatedAt = %q, want %q", first.CreatedAt, steerEpoch.Format(time.RFC3339))
	}

	got, err := s.List("/repo", "COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("List = %+v, want oldest first [%d %d]", got, first.ID, second.ID)
	}
	if got[1].Body != "and the\nsecond line" {
		t.Fatalf("multi-line body not round-tripped: %q", got[1].Body)
	}
}

func TestSteerNoteEmptyQueueReadsAreEmptySlices(t *testing.T) {
	s := testSteerNotes(t)
	notes, err := s.List("/repo", "COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if notes == nil || len(notes) != 0 {
		t.Fatalf("List on an empty queue = %+v, want an empty (non-nil) slice", notes)
	}
	pending, err := s.Pending("/repo", "COD-1")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending == nil || len(pending) != 0 {
		t.Fatalf("Pending on an empty queue = %+v, want an empty (non-nil) slice", pending)
	}
}

func TestSteerNoteAckStampsPhaseThenIsIdempotent(t *testing.T) {
	s := testSteerNotes(t)
	note := queueSteerNote(t, s, "/repo", "COD-1", "rerun the store tests")

	deliveredAt := steerEpoch.Add(time.Minute)
	s.now = func() time.Time { return deliveredAt }
	acked, err := s.Ack("/repo", note.ID, "build")
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if acked.Status != SteerDelivered || acked.DeliveredPhase != "build" {
		t.Fatalf("Ack = %+v, want delivered by build", acked)
	}
	if acked.DeliveredAt != deliveredAt.Format(time.RFC3339) {
		t.Fatalf("DeliveredAt = %q, want %q", acked.DeliveredAt, deliveredAt.Format(time.RFC3339))
	}
	if pending, _ := s.Pending("/repo", "COD-1"); len(pending) != 0 {
		t.Fatalf("delivered note still pending: %+v", pending)
	}

	s.now = func() time.Time { return deliveredAt.Add(time.Hour) }
	again, err := s.Ack("/repo", note.ID, "verify")
	if err != nil {
		t.Fatalf("re-Ack: %v", err)
	}
	if again.DeliveredPhase != "build" || again.DeliveredAt != acked.DeliveredAt {
		t.Fatalf("re-Ack rewrote the first delivery: %+v", again)
	}
}

func TestSteerNoteAckUnknownIDIsScopedToRepo(t *testing.T) {
	s := testSteerNotes(t)
	note := queueSteerNote(t, s, "/repo", "COD-1", "mine")

	if _, err := s.Ack("/repo", note.ID+1, "build"); !errors.Is(err, ErrSteerNoteNotFound) {
		t.Fatalf("Ack(unknown id) = %v, want ErrSteerNoteNotFound", err)
	}
	if _, err := s.Ack("/other", note.ID, "build"); !errors.Is(err, ErrSteerNoteNotFound) {
		t.Fatalf("Ack from another repo = %v, want ErrSteerNoteNotFound", err)
	}
	if pending, _ := s.Pending("/repo", "COD-1"); len(pending) != 1 || pending[0].ID != note.ID {
		t.Fatalf("rejected acks changed the queue: %+v", pending)
	}
}

func TestSteerNoteAckExpiredNoteFails(t *testing.T) {
	s := testSteerNotes(t)
	note := queueSteerNote(t, s, "/repo", "COD-1", "typed too late")
	if _, err := s.Expire("/repo", "COD-1"); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	got, err := s.Ack("/repo", note.ID, "build")
	if !errors.Is(err, ErrSteerNoteExpired) {
		t.Fatalf("Ack(expired) = %v, want ErrSteerNoteExpired", err)
	}
	if got.Status != SteerExpired || got.DeliveredPhase != "" {
		t.Fatalf("late Ack mutated the note: %+v", got)
	}
}

func TestSteerNoteExpireSweepsPendingOnly(t *testing.T) {
	s := testSteerNotes(t)
	consumed := queueSteerNote(t, s, "/repo", "COD-1", "consumed")
	stranded := queueSteerNote(t, s, "/repo", "COD-1", "never read")
	elsewhere := queueSteerNote(t, s, "/repo", "COD-2", "different ticket")
	if _, err := s.Ack("/repo", consumed.ID, "build"); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	swept, err := s.Expire("/repo", "COD-1")
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if len(swept) != 1 || swept[0].ID != stranded.ID || swept[0].Status != SteerExpired {
		t.Fatalf("Expire swept %+v, want only pending note %d", swept, stranded.ID)
	}

	notes, err := s.List("/repo", "COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if notes[0].Status != SteerDelivered || notes[0].DeliveredPhase != "build" {
		t.Fatalf("Expire touched the delivered note: %+v", notes[0])
	}
	if pending, _ := s.Pending("/repo", "COD-2"); len(pending) != 1 || pending[0].ID != elsewhere.ID {
		t.Fatalf("Expire crossed into another ticket: %+v", pending)
	}

	again, err := s.Expire("/repo", "COD-1")
	if err != nil {
		t.Fatalf("second Expire: %v", err)
	}
	if again == nil || len(again) != 0 {
		t.Fatalf("second Expire = %+v, want an empty (non-nil) slice", again)
	}
}

func TestSteerNotesScopedPerRepo(t *testing.T) {
	s := testSteerNotes(t)
	mine := queueSteerNote(t, s, "/repo", "COD-1", "mine")
	theirs := queueSteerNote(t, s, "/other", "COD-1", "theirs")

	got, err := s.List("/repo", "COD-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != mine.ID || got[0].Body != "mine" {
		t.Fatalf("List(/repo) = %+v, want only note %d", got, mine.ID)
	}
	if pending, _ := s.Pending("/other", "COD-1"); len(pending) != 1 || pending[0].ID != theirs.ID {
		t.Fatalf("Pending(/other) = %+v, want only note %d", pending, theirs.ID)
	}

	if _, err := s.Expire("/repo", "COD-1"); err != nil {
		t.Fatalf("Expire(/repo): %v", err)
	}
	if pending, _ := s.Pending("/other", "COD-1"); len(pending) != 1 {
		t.Fatalf("Expire(/repo) swept another repo's queue: %+v", pending)
	}
}
