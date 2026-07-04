package planning

import "testing"

// TestPhaseOrder pins the plan-session lifecycle ranking the resume logic keys
// off: drafting → questions → prd_review → prd_ready → published → sliced, with
// aborted as a terminal side-exit ranked above the forward phases.
func TestPhaseOrder(t *testing.T) {
	forward := []string{PhaseDrafting, PhaseQuestions, PhaseReview, PhasePRDReady, PhasePublished, PhaseSliced}
	for i := 1; i < len(forward); i++ {
		if PhaseRank(forward[i-1]) >= PhaseRank(forward[i]) {
			t.Errorf("phase %q should rank below %q", forward[i-1], forward[i])
		}
	}
	if PhaseRank("nonsense") != 0 {
		t.Errorf("unknown phase should rank 0, got %d", PhaseRank("nonsense"))
	}
	if PhaseRank(PhaseAborted) <= PhaseRank(PhaseSliced) {
		t.Error("aborted should rank above the forward phases")
	}
}

func TestTerminal(t *testing.T) {
	terminal := map[string]bool{
		PhaseDrafting:  false,
		PhaseQuestions: false,
		PhaseReview:    false,
		PhasePRDReady:  false,
		PhasePublished: false,
		PhaseSliced:    true,
		PhaseAborted:   true,
	}
	for phase, want := range terminal {
		if got := Terminal(phase); got != want {
			t.Errorf("Terminal(%q) = %v, want %v", phase, got, want)
		}
	}
}
