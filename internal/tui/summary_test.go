package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

// TestResizeKeepsRecapSelection checks that resizing during the recap keeps the
// queue selection put and still renders — the recap draws through the shared
// attention-queue component, so there is no table to rebuild.
func TestResizeKeepsRecapSelection(t *testing.T) {
	d := freshDash(80, 24, "")
	d.results = []console.TicketResult{
		{ID: "COD-1", Title: "One", Phase: state.PROpen},
		{ID: "COD-2", Title: "Two", Phase: state.Building},
	}
	sm, _ := d.enterSummary(console.SessionSummary{Tickets: 2})
	d = sm.(model)
	d.queueCursor = 1
	sel, ok := d.selectedRow()
	if !ok || sel.ID != "COD-2" {
		t.Fatalf("selected row before resize = %q (ok=%v), want COD-2", sel.ID, ok)
	}

	d = applyDash(d, tea.WindowSizeMsg{Width: 160, Height: 50})

	if got := d.queueCursor; got != 1 {
		t.Errorf("queue cursor after resize = %d, want 1", got)
	}
	if sel, ok := d.selectedRow(); !ok || sel.ID != "COD-2" {
		t.Errorf("selected row after resize = %q (ok=%v), want COD-2", sel.ID, ok)
	}
	if d.render() == "" {
		t.Error("recap rendered empty after resize")
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
