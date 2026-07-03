package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/notify"
	"github.com/RomkaLTU/trau/internal/state"
)

// recapAwayThreshold is the minimum blur duration before a refocus surfaces the
// away-recap banner. Brief tab switches shouldn't nag.
const recapAwayThreshold = 3 * time.Minute

// livePhasePrefix marks a snapshot entry as a live phase (an active step key)
// rather than a terminal state phase, so the two never collide in the recap diff.
const livePhasePrefix = "@"

// ambientTitle renders the terminal tab title from the run's live state so a
// backgrounded trau tab communicates at a glance. It reuses the glyph vocabulary
// the HUD and summary already speak: ✻ working, ⚠ needs attention, ✓ done.
func (m model) ambientTitle() string {
	switch {
	case m.state == stateSummary:
		switch {
		case m.summary.Paused:
			return "trau ⚠ paused"
		case m.summary.Fault:
			return "trau ⚠ needs attention"
		default:
			return fmt.Sprintf("trau ✓ %d merged", m.mergedCount())
		}
	case m.paused:
		return "trau ⚠ paused"
	case m.currentTicket != "":
		if i := activeIndex(m.steps); i >= 0 {
			return fmt.Sprintf("trau ✻ %s %s", m.currentTicket, m.steps[i].key)
		}
		return "trau ✻ " + m.currentTicket
	default:
		return "trau"
	}
}

// resultTally counts the merged and quarantined tickets among the session
// results — the numbers the title, footer, and session notification all report.
func (m model) resultTally() (merged, quarantined int) {
	for i := range m.results {
		switch m.results[i].Phase {
		case state.Merged:
			merged++
		case state.Quarantined:
			quarantined++
		}
	}
	return
}

// mergedCount counts the merged tickets among the session results.
func (m model) mergedCount() int {
	merged, _ := m.resultTally()
	return merged
}

// notifyCmd posts a desktop notification off the render loop. It no-ops when the
// notifier is unset (feature disabled), so callers can compose it unconditionally.
func notifyCmd(n notify.Notifier, title, body string) tea.Cmd {
	if n == nil {
		return nil
	}
	return func() tea.Msg {
		_ = n(title, body)
		return nil
	}
}

// ticketNotifyCmd fires a desktop notification for a ticket that ended in a state
// needing a human — a quarantine. Merged tickets are tallied at session end, so
// they don't each buzz.
func (m model) ticketNotifyCmd(r console.TicketResult) tea.Cmd {
	if r.Phase != state.Quarantined {
		return nil
	}
	body := r.ID + " quarantined"
	if r.FailureReason != "" {
		body += " — " + r.FailureReason
	}
	return notifyCmd(m.notifier, "trau", body)
}

// sessionNotifyCmd fires the single end-of-session desktop notification, its
// message shaped by how the loop stopped: a blameless pause, an unexpected fault,
// or a clean finish with the merged/quarantine tally.
func (m model) sessionNotifyCmd(s console.SessionSummary) tea.Cmd {
	var body string
	switch {
	case s.Paused:
		body = "paused — needs re-auth or the rate limit to clear"
	case s.Fault:
		body = "faulted — resumable WIP preserved"
		if s.FaultID != "" {
			body = "faulted on " + s.FaultID + " — resumable WIP preserved"
		}
	default:
		merged, quar := m.resultTally()
		body = fmt.Sprintf("session ended — %d merged", merged)
		if quar > 0 {
			body += fmt.Sprintf(", %d quarantined", quar)
		}
	}
	return notifyCmd(m.notifier, "trau", body)
}

// recapSnapshot captures each known ticket's phase for the away-recap diff:
// finished tickets by their terminal phase, the live ticket by its active step
// (prefixed so a live phase never collides with a terminal one).
func (m model) recapSnapshot() map[string]string {
	snap := make(map[string]string, len(m.results)+1)
	for i := range m.results {
		snap[m.results[i].ID] = m.results[i].Phase
	}
	if m.currentTicket != "" {
		if i := activeIndex(m.steps); i >= 0 {
			snap[m.currentTicket] = livePhasePrefix + m.steps[i].key
		}
	}
	return snap
}

// buildRecap diffs two snapshots into a one-line "while you were away" summary of
// the state changes since blur. It returns a fixed no-changes line rather than an
// empty string, so a refocus past the away threshold always yields a banner.
func buildRecap(before, after map[string]string) string {
	ids := make([]string, 0, len(after))
	for id := range after {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var parts []string
	for _, id := range ids {
		now := after[id]
		if was, ok := before[id]; ok && was == now {
			continue
		}
		switch {
		case now == state.Merged:
			parts = append(parts, id+" merged")
		case now == state.Quarantined:
			parts = append(parts, id+" quarantined")
		case now == state.PROpen:
			parts = append(parts, id+" reached PR")
		case strings.HasPrefix(now, livePhasePrefix):
			parts = append(parts, id+" reached "+strings.TrimPrefix(now, livePhasePrefix))
		}
	}
	if len(parts) == 0 {
		return "while you were away: no state changes"
	}
	return "while you were away: " + strings.Join(parts, " · ")
}

// onBlur records when focus was lost and a snapshot of run state, so a later
// refocus can diff against it.
func (m *model) onBlur() {
	m.blurAt = time.Now()
	m.blurSnapshot = m.recapSnapshot()
}

// onFocus surfaces the away-recap banner when focus returns after a meaningful
// absence during a live run. Brief blurs, refocus after the run ended, and an
// idle model (no run yet) all fall through without a banner.
func (m *model) onFocus() {
	away, snap := m.blurAt, m.blurSnapshot
	m.blurAt = time.Time{}
	m.blurSnapshot = nil
	if away.IsZero() || time.Since(away) < recapAwayThreshold {
		return
	}
	if m.state != stateRunning || (m.currentTicket == "" && len(m.results) == 0) {
		return
	}
	m.recapBanner = buildRecap(snap, m.recapSnapshot())
}

// withNotifier attaches the desktop notifier the run reports through (nil = the
// feature is off). The app shell injects it into each fresh dashboard.
func (m model) withNotifier(n notify.Notifier) model {
	m.notifier = n
	return m
}

// dismissRecap clears the away-recap banner; any key press retires it.
func (m model) dismissRecap() model {
	m.recapBanner = ""
	return m
}
