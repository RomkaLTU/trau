package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

func steerServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	seedRepo(t, home, "acme")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts.URL + APIPrefix + "/repos/acme/steer"
}

func queueSteerNote(t *testing.T, base, ticket, body string) SteerNoteView {
	t.Helper()
	res := postJSON(t, base, SteerNoteRequest{Ticket: ticket, Body: body})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("queue status = %d, want 201", res.StatusCode)
	}
	var out SteerNoteView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode queued note: %v", err)
	}
	return out
}

func steerList(t *testing.T, base, ticket, status string) []SteerNoteView {
	t.Helper()
	values := url.Values{"ticket": {ticket}}
	if status != "" {
		values.Set("status", status)
	}
	res, err := http.Get(base + "?" + values.Encode())
	if err != nil {
		t.Fatalf("list steer notes: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", res.StatusCode)
	}
	var out SteerNotesResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode steer notes: %v", err)
	}
	return out.Notes
}

// TestSteerQueueAndList covers the queue's arrival contract: a note lands
// pending with its body intact, the timeline read returns a ticket's notes
// oldest-first, the pending filter narrows to the undelivered ones, and one
// ticket's queue never leaks into another's.
func TestSteerQueueAndList(t *testing.T) {
	_, base := steerServer(t)

	first := queueSteerNote(t, base, "COD-1", "the test DB is on now")
	second := queueSteerNote(t, base, "COD-1", "and the\nseed data is loaded")
	queueSteerNote(t, base, "COD-2", "unrelated ticket")

	if first.Status != hubstore.SteerPending || first.DeliveredPhase != "" || first.CreatedAt == "" {
		t.Errorf("queued note = %+v, want a stamped pending note with no phase", first)
	}
	if second.ID <= first.ID {
		t.Errorf("second id = %d, want it after %d", second.ID, first.ID)
	}

	notes := steerList(t, base, "COD-1", "")
	if len(notes) != 2 || notes[0].ID != first.ID || notes[1].Body != "and the\nseed data is loaded" {
		t.Fatalf("timeline = %+v, want both COD-1 notes oldest first", notes)
	}

	ackSteerNote(t, base, first.ID, "build")
	pending := steerList(t, base, "COD-1", hubstore.SteerPending)
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending = %+v, want only the undelivered note", pending)
	}
}

func ackSteerNote(t *testing.T, base string, id int64, phase string) SteerNoteView {
	t.Helper()
	res := postJSON(t, base+"/"+strconv.FormatInt(id, 10)+"/ack", SteerAckRequest{Phase: phase})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ack status = %d, want 200", res.StatusCode)
	}
	var out SteerNoteView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode acked note: %v", err)
	}
	return out
}

// TestSteerAckIsIdempotent covers delivery: an ack stamps the phase that
// consumed the note, and a retried ack returns the note untouched rather than
// rewriting the phase that first took it.
func TestSteerAckIsIdempotent(t *testing.T) {
	_, base := steerServer(t)
	note := queueSteerNote(t, base, "COD-1", "steer me")

	delivered := ackSteerNote(t, base, note.ID, "build")
	if delivered.Status != hubstore.SteerDelivered || delivered.DeliveredPhase != "build" || delivered.DeliveredAt == "" {
		t.Fatalf("acked note = %+v, want delivered in build with a timestamp", delivered)
	}

	again := ackSteerNote(t, base, note.ID, "verify")
	if again.DeliveredPhase != "build" || again.DeliveredAt != delivered.DeliveredAt {
		t.Errorf("re-acked note = %+v, want the first delivery preserved", again)
	}
}

// TestSteerExpireSweepsPendingOnly covers the settle path: the sweep expires
// what is still queued, leaves delivered notes alone, reports what it swept, and
// repeats harmlessly. An expired note then refuses a late ack.
func TestSteerExpireSweepsPendingOnly(t *testing.T) {
	_, base := steerServer(t)
	delivered := queueSteerNote(t, base, "COD-1", "consumed")
	stranded := queueSteerNote(t, base, "COD-1", "never read")
	other := queueSteerNote(t, base, "COD-2", "other ticket")
	ackSteerNote(t, base, delivered.ID, "build")

	swept := expireSteer(t, base, "COD-1")
	if len(swept) != 1 || swept[0].ID != stranded.ID || swept[0].Status != hubstore.SteerExpired {
		t.Fatalf("swept = %+v, want only the pending COD-1 note", swept)
	}
	if again := expireSteer(t, base, "COD-1"); len(again) != 0 {
		t.Errorf("second sweep = %+v, want nothing left to expire", again)
	}
	if pending := steerList(t, base, "COD-2", hubstore.SteerPending); len(pending) != 1 || pending[0].ID != other.ID {
		t.Errorf("COD-2 pending = %+v, want the other ticket untouched", pending)
	}

	notes := steerList(t, base, "COD-1", "")
	if notes[0].Status != hubstore.SteerDelivered || notes[0].DeliveredPhase != "build" {
		t.Errorf("delivered note after sweep = %+v, want it untouched", notes[0])
	}

	res := postJSON(t, base+"/"+strconv.FormatInt(stranded.ID, 10)+"/ack", SteerAckRequest{Phase: "verify"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("late ack status = %d, want 409", res.StatusCode)
	}
}

func expireSteer(t *testing.T, base, ticket string) []SteerNoteView {
	t.Helper()
	res := postJSON(t, base+"/expire", SteerExpireRequest{Ticket: ticket})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expire status = %d, want 200", res.StatusCode)
	}
	var out SteerNotesResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode expired notes: %v", err)
	}
	return out.Notes
}

func TestSteerRejectsBadRequests(t *testing.T) {
	_, base := steerServer(t)
	note := queueSteerNote(t, base, "COD-1", "queued")

	cases := []struct {
		name string
		path string
		body any
		want int
	}{
		{"empty body", "", SteerNoteRequest{Ticket: "COD-1", Body: ""}, http.StatusBadRequest},
		{"whitespace body", "", SteerNoteRequest{Ticket: "COD-1", Body: " \n\t "}, http.StatusBadRequest},
		{"no ticket", "", SteerNoteRequest{Body: "orphan"}, http.StatusBadRequest},
		{"ack without phase", "/" + strconv.FormatInt(note.ID, 10) + "/ack", SteerAckRequest{}, http.StatusBadRequest},
		{"ack unknown note", "/9999/ack", SteerAckRequest{Phase: "build"}, http.StatusNotFound},
		{"ack non-numeric id", "/nope/ack", SteerAckRequest{Phase: "build"}, http.StatusBadRequest},
		{"expire without ticket", "/expire", SteerExpireRequest{}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := postJSON(t, base+c.path, c.body)
			_ = res.Body.Close()
			if res.StatusCode != c.want {
				t.Errorf("status = %d, want %d", res.StatusCode, c.want)
			}
		})
	}

	res, err := http.Get(base)
	if err != nil {
		t.Fatalf("list without ticket: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("list without ticket status = %d, want 400", res.StatusCode)
	}
}

// TestSteerQueueEmitsEvent covers the hub-side half of the steer vocabulary: the
// queue writer is the hub, so it — not the child — records that a note was typed.
func TestSteerQueueEmitsEvent(t *testing.T) {
	s, base := steerServer(t)
	note := queueSteerNote(t, base, "COD-1", "the test DB is on now")

	repo, ok := s.findRepo("acme")
	if !ok {
		t.Fatal("acme not registered")
	}
	rows, err := s.stores.Events().Recent(repo.Root, 10, 0)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(rows) != 1 || rows[0].Kind != event.KindSteerQueued {
		t.Fatalf("events = %+v, want one %s", rows, event.KindSteerQueued)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(rows[0].Fields), &fields); err != nil {
		t.Fatalf("decode fields: %v", err)
	}
	if fields["ticket"] != "COD-1" || fields["note_id"] != float64(note.ID) {
		t.Errorf("fields = %+v, want the ticket and note id", fields)
	}
	if rows[0].Msg == "" || fields["body"] != nil {
		t.Errorf("event = %+v, want a message and no note body in the feed", rows[0])
	}
}

// TestSteerRoutesRequireToken proves the steer routes inherit the fail-closed
// gate rather than opening a new hole in an exposed hub.
func TestSteerRoutesRequireToken(t *testing.T) {
	ts := exposedServer(t, "0.0.0.0", "tok")
	base := APIPrefix + "/repos/acme/steer"
	for _, path := range []string{base, base + "/expire", base + "/1/ack"} {
		if got := statusWithToken(t, ts, path, ""); got != http.StatusUnauthorized {
			t.Errorf("GET %s without token = %d, want 401", path, got)
		}
	}
}
