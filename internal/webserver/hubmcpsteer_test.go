package webserver

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

func steerAgent(t *testing.T, ts *httptest.Server, args map[string]any) MCPSteerNote {
	t.Helper()
	var note MCPSteerNote
	hubToolPayload(t, hubTool(t, ts, "steer_agent", args), &note)
	return note
}

func mcpSteerList(t *testing.T, ts *httptest.Server, args map[string]any) MCPSteerNotes {
	t.Helper()
	var notes MCPSteerNotes
	hubToolPayload(t, hubTool(t, ts, "list_steer_notes", args), &notes)
	return notes
}

// TestHubMCPSteerAgentQueuesLikeTheRoute proves steer_agent takes the queue path
// the web UI takes rather than a parallel one: the note it writes is
// indistinguishable in the store from one typed through the REST route, body and
// all, and it records the same queued event.
func TestHubMCPSteerAgentQueuesLikeTheRoute(t *testing.T) {
	ts, s, root := hubMCPServer(t)
	rest := ts.URL + APIPrefix + "/repos/acme/steer"
	const body = "also update the changelog\nand the docs"

	queued := steerAgent(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1", "body": body})
	if queued.Repo != "acme" || queued.Ticket != "ACME-1" || queued.Status != hubstore.SteerPending {
		t.Fatalf("steer_agent = %+v, want a pending note for ACME-1 in acme", queued)
	}
	if queued.ID == 0 || queued.CreatedAt == "" || queued.DeliveredPhase != "" {
		t.Errorf("steer_agent = %+v, want a stamped note nothing has delivered yet", queued)
	}

	viaRoute := queueSteerNote(t, rest, "ACME-1", body)

	notes, err := s.stores.Steer().List(root, "ACME-1")
	if err != nil {
		t.Fatalf("list steer notes: %v", err)
	}
	if len(notes) != 2 || notes[0].ID != queued.ID || notes[1].ID != viaRoute.ID {
		t.Fatalf("stored notes = %+v, want the tool's note then the route's", notes)
	}
	forget := func(n hubstore.SteerNote) hubstore.SteerNote {
		n.ID, n.CreatedAt = 0, ""
		return n
	}
	if forget(notes[0]) != forget(notes[1]) {
		t.Errorf("tool note %+v is not the note %+v the route writes", notes[0], notes[1])
	}
	if notes[0].Body != body {
		t.Errorf("stored body = %q, want the multi-line note verbatim", notes[0].Body)
	}

	rows, err := s.stores.Events().Recent(root, 10, 0)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	steered := 0
	for _, row := range rows {
		if row.Kind == event.KindSteerQueued {
			steered++
		}
	}
	if steered != 2 {
		t.Errorf("%s events = %d, want one per queued note", event.KindSteerQueued, steered)
	}
}

// TestHubMCPListSteerNotesTracksStatus covers what an agent watching its own
// nudge sees: pending until a phase consumes it, delivered with that phase after,
// and expired once the run settles with a note still queued.
func TestHubMCPListSteerNotesTracksStatus(t *testing.T) {
	ts, _, _ := hubMCPServer(t)
	rest := ts.URL + APIPrefix + "/repos/acme/steer"

	taken := steerAgent(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1", "body": "run the linter too"})
	stranded := steerAgent(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1", "body": "and the changelog"})
	steerAgent(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-2", "body": "a note for another ticket"})

	ackSteerNote(t, rest, taken.ID, "build")

	all := mcpSteerList(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1"})
	if all.Repo != "acme" || all.Ticket != "ACME-1" || len(all.Notes) != 2 {
		t.Fatalf("list_steer_notes = %+v, want both ACME-1 notes oldest first", all)
	}
	delivered := all.Notes[0]
	if delivered.ID != taken.ID || delivered.Status != hubstore.SteerDelivered {
		t.Fatalf("first note = %+v, want the acked note delivered", delivered)
	}
	if delivered.DeliveredPhase != "build" || delivered.DeliveredAt == "" {
		t.Errorf("delivered note = %+v, want the phase that consumed it and when", delivered)
	}

	waiting := mcpSteerList(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1", "pending_only": true})
	if len(waiting.Notes) != 1 || waiting.Notes[0].ID != stranded.ID {
		t.Fatalf("pending_only = %+v, want only the undelivered note", waiting.Notes)
	}

	expireSteer(t, rest, "ACME-1")

	settled := mcpSteerList(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1"})
	if settled.Notes[1].Status != hubstore.SteerExpired || settled.Notes[0].Status != hubstore.SteerDelivered {
		t.Errorf("after the sweep = %+v, want the stranded note expired and the delivered one untouched", settled.Notes)
	}
	if left := mcpSteerList(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-1", "pending_only": true}); len(left.Notes) != 0 {
		t.Errorf("pending after the sweep = %+v, want nothing still waiting", left.Notes)
	}
	other := mcpSteerList(t, ts, map[string]any{"repo": "acme", "ticket": "ACME-2", "pending_only": true})
	if len(other.Notes) != 1 {
		t.Errorf("ACME-2 pending = %+v, want the other ticket's queue untouched", other.Notes)
	}
}

func TestHubMCPSteerBadArguments(t *testing.T) {
	ts, s, root := hubMCPServer(t)

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"steer without a ticket", "steer_agent", map[string]any{"repo": "acme", "body": "nudge"}, "ticket is required"},
		{"steer without a body", "steer_agent", map[string]any{"repo": "acme", "ticket": "ACME-1"}, "needs a body"},
		{"steer with a blank body", "steer_agent", map[string]any{"repo": "acme", "ticket": "ACME-1", "body": " \n\t "}, "needs a body"},
		{"steer an unknown repo", "steer_agent", map[string]any{"repo": "nope", "ticket": "ACME-1", "body": "nudge"}, "unknown repo"},
		{"list without a ticket", "list_steer_notes", map[string]any{"repo": "acme"}, "ticket is required"},
		{"list an unknown repo", "list_steer_notes", map[string]any{"repo": "nope", "ticket": "ACME-1"}, "unknown repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := hubTool(t, ts, tc.tool, tc.args)
			if !tr.IsError || !strings.Contains(tr.Content[0].Text, tc.want) {
				t.Fatalf("%s result = %+v, want a tool error mentioning %q", tc.tool, tr, tc.want)
			}
		})
	}

	notes, err := s.stores.Steer().List(root, "ACME-1")
	if err != nil {
		t.Fatalf("list steer notes: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("stored notes = %+v, want a refused steer to queue nothing", notes)
	}
}
