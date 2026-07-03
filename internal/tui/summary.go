package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/state"
)

// statusKind buckets a ticket's terminal phase for coloring.
type statusKind int

const (
	statusOK   statusKind = iota // merged
	statusWarn                   // PR open, awaiting human merge
	statusBad                    // quarantined / incomplete
)

// phaseReset is a synthetic phase set on a summary row after the user resets that
// ticket from the recap, so the row reflects the manual action (it is never a real
// checkpoint).
const phaseReset = "·reset·"

// statusLabel maps a final state checkpoint to a human label + kind.
func statusLabel(phase string) (string, statusKind) {
	switch phase {
	case state.Merged:
		return "merged", statusOK
	case state.PROpen:
		return "PR open", statusWarn
	case state.Quarantined:
		return "quarantined", statusBad
	case phaseReset:
		return "reset", statusWarn
	default:
		return "incomplete", statusBad
	}
}

func statusGlyph(k statusKind) string {
	switch k {
	case statusOK:
		return "✓"
	case statusWarn:
		return "◔"
	default:
		return "⚠"
	}
}

// enterSummary flips the model to the completion screen. It deliberately does
// NOT quit — the program stays up showing the recap until the user dismisses it
// (the old behavior quit in the same frame, so the summary never appeared).
func (m model) enterSummary(s console.SessionSummary) (tea.Model, tea.Cmd) {
	m.state = stateSummary
	m.summary = s
	m.banner = ""
	m.queueCursor = 0
	return m, nil
}

// resultRows projects the session's ticket results onto the shared queue model,
// so the recap renders through the same attention-sorted component as the live
// rail and the Status screen. Age is the measured per-ticket elapsed.
func (m model) resultRows() []QueueRow {
	rows := make([]QueueRow, 0, len(m.results))
	for _, r := range m.results {
		rows = append(rows, QueueRow{
			ID:            r.ID,
			Title:         r.Title,
			Phase:         r.Phase,
			PRURL:         r.PRURL,
			Branch:        r.Branch,
			FailureReason: r.FailureReason,
			Tokens:        r.Tokens,
			Cost:          r.Cost,
			CostMetered:   r.CostMetered,
			Age:           r.Elapsed,
		})
	}
	return rows
}

// queueRows is the row set the cursor and renderer operate on: the recap draws
// from the session results, the live rail from the store-backed snapshot with the
// active ticket overlaid live.
func (m model) queueRows() []QueueRow {
	if m.state == stateSummary {
		return m.resultRows()
	}
	return m.liveQueueRows()
}

// liveQueueRows overlays the running ticket onto the store snapshot: its row is
// marked Live (so it animates and floats up its bucket), shows its precise active
// phase, and ages from when it started. The active ticket is injected if the
// store has no checkpoint for it yet, so the rail always shows what's running.
func (m model) liveQueueRows() []QueueRow {
	rows := make([]QueueRow, len(m.queue))
	copy(rows, m.queue)
	if m.currentTicket == "" {
		return rows
	}
	found := false
	for i := range rows {
		if rows[i].ID != m.currentTicket {
			continue
		}
		found = true
		rows[i].Live = true
		rows[i].FailureReason = "" // the active ticket is running, not failed
		if d := m.activePhase(); d != "" {
			rows[i].Desc = d
		}
		if !m.ticketStarted.IsZero() {
			rows[i].Age = time.Since(m.ticketStarted)
		}
	}
	if !found {
		var age time.Duration
		if !m.ticketStarted.IsZero() {
			age = time.Since(m.ticketStarted)
		}
		rows = append(rows, QueueRow{
			ID:    m.currentTicket,
			Title: m.currentTitle,
			Phase: state.Building,
			Live:  true,
			Desc:  m.activePhase(),
			Age:   age,
		})
	}
	return rows
}

// selectableCount is the number of rows the queue cursor can land on (the
// non-folded rows), shared by the recap and the live rail.
func (m model) selectableCount() int {
	active, _ := partitionQueue(m.queueRows())
	return len(active)
}

// selectedRow returns the queue row under the cursor, or false when the queue is
// empty. The row set is the recap results, or the live rail snapshot while a run
// is in progress.
func (m model) selectedRow() (QueueRow, bool) {
	active, _ := partitionQueue(m.queueRows())
	if m.queueCursor < 0 || m.queueCursor >= len(active) {
		return QueueRow{}, false
	}
	return active[m.queueCursor], true
}

// spinFrame is the current spinner glyph, stripped of styling, for animating the
// live row in the shared queue renderer.
func (m model) spinFrame() string {
	return spinnerGlyph(m.spin)
}

// renderSummary draws the centered completion card with totals + the attention
// queue rendered through the shared component.
func (m model) renderSummary() string {
	title := m.styles.SummaryTitle.Render("trau · session complete")

	var head string
	switch {
	case m.summary.Fault:
		id := firstNonEmpty(m.summary.FaultID, "the ticket")
		phase := firstNonEmpty(m.summary.FaultPhase, "a phase")
		head = m.styles.Subtle.Render(m.totalsLine()) + "\n" +
			m.styles.Warning.Render(fmt.Sprintf("⚠ %s couldn't finish during %s — work saved on its branch; rerun trau to resume", id, phase))
	case m.summary.Paused:
		reason := "provider rate/usage limit reached"
		if m.summary.Err != nil {
			reason = m.summary.Err.Error()
		}
		head = m.styles.Subtle.Render(m.totalsLine()) + "\n" +
			m.styles.Warning.Render("⏸ "+reason+" — work saved; rerun trau to resume")
	case m.summary.Err != nil:
		head = m.styles.Error.Render("aborted: " + m.summary.Err.Error())
	default:
		head = m.styles.Subtle.Render(m.totalsLine())
	}

	body := title + "\n" + head
	if len(m.results) > 0 {
		queueW := m.width - 8
		if queueW < 24 {
			queueW = 24
		}
		// Leave room for the card chrome, title, totals, and note so the queue
		// (with always-shown failure reasons) never overflows a short terminal.
		queueH := m.height - 12
		if queueH < 4 {
			queueH = 4
		}
		body += "\n\n" + renderQueue(m.styles, m.spinFrame(), m.resultRows(), m.queueCursor, queueW, queueH)
	}
	if m.recoveryNote != "" {
		body += "\n\n" + m.styles.Subtle.Render(m.recoveryNote)
	}
	return cardView(m.styles, m.width, m.height, body, m.summaryHint())
}

// summaryHint is the recap's key legend, built from the shared queue verbs. The
// recap is not "live", so the full recovery set (resume/checkout) is offered; the
// closing key is spelled esc/q here rather than the rail's reconcile.
func (m model) summaryHint() string {
	if m.confirmResetID != "" {
		return "⚠ reset " + m.confirmResetID + "? x again to confirm · esc cancel"
	}
	sel, hasSel := m.selectedRow()
	parts := append([]string{"↑↓ move"}, queueVerbHints(sel, hasSel, false)...)
	parts = append(parts, "esc/q close")
	return strings.Join(parts, " · ")
}

// recoverable reports whether a ticket result still has work to act on — i.e. it
// is neither merged nor already reset, so resume/reset/checkout make sense.
func recoverable(r console.TicketResult) bool {
	return r.Phase != state.Merged && r.Phase != phaseReset
}

// totalsLine summarizes the session: counts by outcome, elapsed, cost, tokens.
// Each outcome gets its own count so an unfinished ticket reads as "incomplete"
// rather than being lumped under "quarantined" — only a genuinely quarantined
// ticket (PHASE=quarantined) is counted as such.
func (m model) totalsLine() string {
	merged, inReview, incomplete, quarantined := 0, 0, 0, 0
	for i := range m.results {
		switch m.results[i].Phase {
		case state.Merged:
			merged++
		case state.PROpen:
			inReview++
		case state.Quarantined:
			quarantined++
		case phaseReset:
			// reset from the recap — not an outcome of the run itself
		default:
			incomplete++
		}
	}
	noun := "tickets"
	if m.summary.Tickets == 1 {
		noun = "ticket"
	}
	parts := []string{fmt.Sprintf("%d %s", m.summary.Tickets, noun)}
	if merged > 0 {
		parts = append(parts, m.styles.Success.Render(fmt.Sprintf("%d merged", merged)))
	}
	if inReview > 0 {
		parts = append(parts, m.styles.Warning.Render(fmt.Sprintf("%d in review", inReview)))
	}
	if incomplete > 0 {
		parts = append(parts, m.styles.Warning.Render(fmt.Sprintf("%d incomplete", incomplete)))
	}
	if quarantined > 0 {
		parts = append(parts, m.styles.Error.Render(fmt.Sprintf("%d quarantined", quarantined)))
	}
	parts = append(parts, fmtDur(m.summary.Elapsed))
	parts = append(parts, costSummaryTUI(m.summary.TotalCost, m.summary.CostMetered))
	if m.summary.TotalTokens > 0 {
		parts = append(parts, fmtTokens(m.summary.TotalTokens)+" tokens")
	}
	return strings.Join(parts, " · ")
}

// costSummaryTUI renders the closing session total: "~$X est" when metered,
// "cost n/a" when nothing was measured, "~$X+ est" for an unmetered lower bound.
func costSummaryTUI(cost float64, metered bool) string {
	if metered {
		return fmt.Sprintf("~$%s est", strconv.FormatFloat(cost, 'f', 2, 64))
	}
	if cost == 0 {
		return "cost n/a"
	}
	return fmt.Sprintf("~$%s+ est", strconv.FormatFloat(cost, 'f', 2, 64))
}

// openSelectedPR opens the focused ticket's PR in the browser, if it has one.
func (m model) openSelectedPR() tea.Cmd {
	sel, ok := m.selectedRow()
	if !ok || sel.PRURL == "" {
		return nil
	}
	return openURLCmd(sel.PRURL)
}

// fmtTokens renders a token count compactly: 705 → "705", 11137 → "11.1k".
func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 2, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.Itoa(n)
	}
}

func firstNonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
