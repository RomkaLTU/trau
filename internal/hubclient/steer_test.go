package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQueueSteerPostsNoteAndDecodesIt(t *testing.T) {
	var got steerTicketBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/steer" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeJSON(w, http.StatusCreated, SteerNote{ID: 7, Ticket: got.Ticket, Body: got.Body, Status: SteerPending, CreatedAt: "2026-07-22T10:00:00Z"})
	}))
	defer ts.Close()

	note, err := New(ts.URL, "").QueueSteer(context.Background(), "acme", "COD-1", "the test DB is on now")
	if err != nil {
		t.Fatalf("QueueSteer: %v", err)
	}
	if got.Ticket != "COD-1" || got.Body != "the test DB is on now" {
		t.Errorf("hub received %+v", got)
	}
	if note.ID != 7 || note.Status != SteerPending || note.CreatedAt == "" {
		t.Errorf("note = %+v, want the hub's pending note", note)
	}
}

// The two read variants must differ only by the status filter the child's poll
// narrows with — the timeline sends none.
func TestSteerNotesReadVariants(t *testing.T) {
	cases := []struct {
		name       string
		read       func(*Client) ([]SteerNote, error)
		wantStatus string
	}{
		{
			name: "timeline",
			read: func(c *Client) ([]SteerNote, error) { return c.SteerNotes(context.Background(), "acme", "COD-1") },
		},
		{
			name: "pending poll",
			read: func(c *Client) ([]SteerNote, error) {
				return c.PendingSteerNotes(context.Background(), "acme", "COD-1")
			},
			wantStatus: SteerPending,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != apiPrefix+"/repos/acme/steer" {
					t.Errorf("path = %q", r.URL.Path)
				}
				q := r.URL.Query()
				if q.Get("ticket") != "COD-1" || q.Get("status") != c.wantStatus {
					t.Errorf("query = %v, want ticket COD-1 and status %q", q, c.wantStatus)
				}
				writeJSON(w, http.StatusOK, steerNotesBody{Notes: []SteerNote{
					{ID: 1, Ticket: "COD-1", Body: "first", Status: SteerPending},
					{ID: 2, Ticket: "COD-1", Body: "second", Status: SteerPending},
				}})
			}))
			defer ts.Close()

			notes, err := c.read(New(ts.URL, ""))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(notes) != 2 || notes[0].ID != 1 || notes[1].Body != "second" {
				t.Errorf("notes = %+v, want both in queue order", notes)
			}
		})
	}
}

func TestAckSteerPostsPhaseToNote(t *testing.T) {
	var got steerAckBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/steer/7/ack" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeJSON(w, http.StatusOK, SteerNote{ID: 7, Status: "delivered", DeliveredPhase: got.Phase})
	}))
	defer ts.Close()

	if err := New(ts.URL, "").AckSteer(context.Background(), "acme", 7, "build"); err != nil {
		t.Fatalf("AckSteer: %v", err)
	}
	if got.Phase != "build" {
		t.Errorf("hub received phase %q, want build", got.Phase)
	}
}

func TestAckSteerSurfacesExpiredConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "steer note already expired"})
	}))
	defer ts.Close()

	err := New(ts.URL, "").AckSteer(context.Background(), "acme", 7, "build")
	if err == nil || !strings.Contains(err.Error(), "already expired") {
		t.Fatalf("AckSteer err = %v, want the hub's conflict message", err)
	}
}

func TestExpireSteerReturnsSweptNotes(t *testing.T) {
	var got steerTicketBody
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPrefix+"/repos/acme/steer/expire" {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeJSON(w, http.StatusOK, steerNotesBody{Notes: []SteerNote{{ID: 3, Ticket: got.Ticket, Status: "expired"}}})
	}))
	defer ts.Close()

	swept, err := New(ts.URL, "").ExpireSteer(context.Background(), "acme", "COD-1")
	if err != nil {
		t.Fatalf("ExpireSteer: %v", err)
	}
	if got.Ticket != "COD-1" || got.Body != "" {
		t.Errorf("hub received %+v, want the ticket alone", got)
	}
	if len(swept) != 1 || swept[0].ID != 3 || swept[0].Status != "expired" {
		t.Errorf("swept = %+v, want the one expired note", swept)
	}
}

func TestSteerSendsBearer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		writeJSON(w, http.StatusOK, steerNotesBody{})
	}))
	defer ts.Close()

	if _, err := New(ts.URL, "tok").SteerNotes(context.Background(), "acme", "COD-1"); err != nil {
		t.Fatalf("SteerNotes: %v", err)
	}
}
