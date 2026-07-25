package main

import (
	"testing"

	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/state"
)

// A pinned run resumes only its own prior progress; a bare loop run keeps the
// global finish-what-you-started scan.
func TestRealEngineResumeTargetScope(t *testing.T) {
	cps := state.NewStore(t.TempDir())
	for id, phase := range map[string]string{"COD-1242": state.Built, "COD-1243": state.HandedOff} {
		if err := cps.Set(id, "PHASE", phase); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		keep      func(string) bool
		wantID    string
		wantPhase string
	}{
		{"pinned to a ticket with prior progress", forcedTicketFilter("COD-1243"), "COD-1243", state.HandedOff},
		{"pinned to a ticket with none", forcedTicketFilter("COD-1244"), "", ""},
		{"bare loop run scans every checkpoint", nil, "COD-1242", state.Built},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &realEngine{pipe: &pipeline.Pipeline{State: cps}, resumeKeep: tc.keep}

			id, phase := eng.ResumeTarget()

			if id != tc.wantID || phase != tc.wantPhase {
				t.Fatalf("ResumeTarget = (%q, %q), want (%q, %q)", id, phase, tc.wantID, tc.wantPhase)
			}
		})
	}
}

func TestForcedTicketFilterAcceptsOnlyTheForcedTicket(t *testing.T) {
	keep := forcedTicketFilter("COD-1243")
	for _, id := range []string{"COD-1243", " cod-1243 "} {
		if !keep(id) {
			t.Errorf("keep(%q) = false, want the forced ticket accepted", id)
		}
	}
	for _, id := range []string{"COD-1242", "COD-12433", ""} {
		if keep(id) {
			t.Errorf("keep(%q) = true, want only the forced ticket accepted", id)
		}
	}
}
