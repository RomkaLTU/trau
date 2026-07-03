package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/RomkaLTU/trau/internal/event"
)

// TestApplyEventDrivesPRBadge asserts the pr_open/ci/tickets events land on the
// model fields the header reads, including the "open" default before any CI verdict.
func TestApplyEventDrivesPRBadge(t *testing.T) {
	m := freshDash(120, 40, "main")

	m.applyEvent(event.Event{Kind: "pr_open", Fields: map[string]any{"number": 71, "url": "https://github.com/o/r/pull/71"}})
	if m.prNum != 71 || m.prURL == "" {
		t.Fatalf("pr_open not applied: num=%d url=%q", m.prNum, m.prURL)
	}
	if m.ciState != "open" {
		t.Fatalf("ciState after pr_open = %q, want open", m.ciState)
	}

	m.applyEvent(event.Event{Kind: "ci", Fields: map[string]any{"state": "pending", "poll_secs": 30}})
	if m.ciState != "pending" || m.ciEvery != 30 || m.ciPollAt.IsZero() {
		t.Fatalf("ci pending not applied: state=%q every=%d at=%v", m.ciState, m.ciEvery, m.ciPollAt)
	}

	m.applyEvent(event.Event{Kind: "tickets", Fields: map[string]any{"total": 8}})
	if m.plannedTotal != 8 {
		t.Fatalf("plannedTotal = %d, want 8", m.plannedTotal)
	}
}

// TestPRBadgeColorsByCIState checks the badge renders the PR number for every
// state and that each CI verdict paints a distinct color (so the header actually
// reflects checks progressing).
func TestPRBadgeColorsByCIState(t *testing.T) {
	setThemeBackground(true)
	m := freshDash(120, 40, "")
	m.prNum = 71

	seen := map[string]string{}
	for _, st := range []string{"open", "pending", "failing", "green", "merged"} {
		m.ciState = st
		badge := m.prBadge()
		if !strings.Contains(ansi.Strip(badge), "PR #71") {
			t.Fatalf("badge for %q missing label: %q", st, ansi.Strip(badge))
		}
		for prev, raw := range seen {
			if raw == badge {
				t.Fatalf("badge color for %q is identical to %q", st, prev)
			}
		}
		seen[st] = badge
	}

	m.prNum = 0
	if m.prBadge() != "" {
		t.Fatal("badge should be empty with no PR")
	}
}

// TestTicketCounter covers the planned-set (n/N) vs queue (plain n) forms.
func TestTicketCounter(t *testing.T) {
	m := freshDash(120, 40, "")
	if got := m.ticketCounter(); got != "" {
		t.Fatalf("counter before first pick = %q, want empty", got)
	}
	m.ticketNum = 2
	if got := m.ticketCounter(); got != "ticket 2" {
		t.Fatalf("queue counter = %q, want %q", got, "ticket 2")
	}
	m.plannedTotal = 5
	if got := m.ticketCounter(); got != "ticket 2/5" {
		t.Fatalf("planned counter = %q, want %q", got, "ticket 2/5")
	}
}

// TestCICountdownWhilePending asserts the state chip surfaces the CI wait with a
// countdown derived from the poll cadence.
func TestCICountdownWhilePending(t *testing.T) {
	m := freshDash(120, 40, "")
	m.ciState = "pending"
	m.ciEvery = 30
	m.ciPollAt = time.Now()
	label, _ := m.stateChip()
	if !strings.HasPrefix(label, "CI next ") {
		t.Fatalf("state chip = %q, want CI countdown", label)
	}
	// Unknown cadence degrades to a bare CI label rather than a bogus timer.
	m.ciEvery = 0
	if label, _ := m.stateChip(); label != "CI" {
		t.Fatalf("state chip with no cadence = %q, want %q", label, "CI")
	}
}

// TestHeaderTitleYieldsFirstAt80Cols verifies the fixed elements (brand, ticket,
// PR badge, elapsed) survive an 80-col header while a long title is the element
// that truncates.
func TestHeaderTitleYieldsFirstAt80Cols(t *testing.T) {
	setThemeBackground(true)
	m := freshDash(80, 24, "main")
	m.currentTicket = "COD-668"
	m.currentTitle = "Richer run header with a deliberately very long ticket title that cannot fit"
	m.ticketNum = 2
	m.plannedTotal = 5
	m.prNum = 71
	m.ciState = "pending"

	plain := ansi.Strip(m.renderHeader())
	if w := lipgloWidthFirstLine(plain); w > 80 {
		t.Fatalf("header first line = %d cols, want <= 80:\n%s", w, plain)
	}
	for _, want := range []string{"trau", "COD-668", "ticket 2/5", "PR #71"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("header dropped fixed element %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "…") {
		t.Fatalf("long title should have truncated with an ellipsis:\n%s", plain)
	}
}

// lipgloWidthFirstLine returns the display width of a rendered block's first line.
func lipgloWidthFirstLine(s string) int {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return ansi.StringWidth(s)
}
