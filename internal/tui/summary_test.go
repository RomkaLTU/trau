package tui

import (
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/state"
)

// TestTotalsLineCountsIncompleteSeparately guards the COD-498 display bug: an
// unfinished ticket (PHASE not quarantined) must read as "incomplete" in the
// header, never be lumped into the "quarantined" count.
func TestTotalsLineCountsIncompleteSeparately(t *testing.T) {
	m := model{
		styles:  DefaultStyles(),
		summary: console.SessionSummary{Tickets: 3},
		results: []console.TicketResult{
			{ID: "COD-1", Phase: state.Merged},
			{ID: "COD-2", Phase: state.Building},
			{ID: "COD-3", Phase: state.Quarantined},
		},
	}

	got := m.totalsLine()

	for _, want := range []string{"1 merged", "1 incomplete", "1 quarantined"} {
		if !strings.Contains(got, want) {
			t.Errorf("totalsLine = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "2 quarantined") {
		t.Errorf("totalsLine = %q: an incomplete ticket must not be counted as quarantined", got)
	}
}

// TestRecoverableExcludesTerminalRows checks the predicate that gates the recovery
// keys: resume/reset/checkout apply to unfinished work, not to merged or
// already-reset rows.
func TestRecoverableExcludesTerminalRows(t *testing.T) {
	cases := []struct {
		phase string
		want  bool
	}{
		{state.Building, true},
		{state.Quarantined, true},
		{state.PROpen, true},
		{state.Merged, false},
		{phaseReset, false},
	}
	for _, c := range cases {
		if got := recoverable(console.TicketResult{Phase: c.phase}); got != c.want {
			t.Errorf("recoverable(%q) = %v, want %v", c.phase, got, c.want)
		}
	}
}
