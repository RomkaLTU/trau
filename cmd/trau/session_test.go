package main

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestSessionRecorderWritesActiveTicketCheckpoint pins the takeover handle
// wiring (ADR 0018): a claude phase's session id lands on the active ticket's
// checkpoint as SESSION/SESSION_PHASE, tracker calls (which share the claude
// backends but route to the pick bucket) never touch the fields, and push-repair
// — the one pick-bucketed real phase — still records.
func TestSessionRecorderWritesActiveTicketCheckpoint(t *testing.T) {
	cps := state.NewStore(t.TempDir())
	rec := &sessionRecorder{}
	rec.bind(cps)

	rec.record("uuid-early", "build")
	if got := cps.Get("COD-1", "SESSION"); got != "" {
		t.Errorf("SESSION = %q before any ticket is active, want empty", got)
	}

	rec.setTicket("COD-1")
	rec.record("uuid-build", "build")
	if got := cps.Get("COD-1", "SESSION"); got != "uuid-build" {
		t.Errorf("SESSION = %q, want uuid-build", got)
	}
	if got := cps.Get("COD-1", "SESSION_PHASE"); got != "build" {
		t.Errorf("SESSION_PHASE = %q, want build", got)
	}

	for _, label := range []string{"pick", "status", "title", "file_bug"} {
		rec.record("uuid-tracker", label)
	}
	if got := cps.Get("COD-1", "SESSION"); got != "uuid-build" {
		t.Errorf("SESSION = %q after tracker calls, want uuid-build untouched", got)
	}

	rec.record("uuid-repair", "repair2")
	if got, want := cps.Get("COD-1", "SESSION"), "uuid-repair"; got != want {
		t.Errorf("SESSION = %q, want %q (most recent claude phase wins)", got, want)
	}
	if got := cps.Get("COD-1", "SESSION_PHASE"); got != "repair2" {
		t.Errorf("SESSION_PHASE = %q, want repair2", got)
	}

	rec.record("uuid-push", "push-repair1")
	if got := cps.Get("COD-1", "SESSION"); got != "uuid-push" {
		t.Errorf("SESSION = %q, want uuid-push (push-repair is a real phase)", got)
	}

	rec.setTicket("COD-2")
	rec.record("uuid-next", "build")
	if got := cps.Get("COD-2", "SESSION"); got != "uuid-next" {
		t.Errorf("COD-2 SESSION = %q, want uuid-next", got)
	}
	if got := cps.Get("COD-1", "SESSION"); got != "uuid-push" {
		t.Errorf("COD-1 SESSION = %q, want the previous ticket left untouched", got)
	}
}
