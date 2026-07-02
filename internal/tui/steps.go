package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// stepState is the lifecycle of one pipeline phase as the stepper sees it.
type stepState int

const (
	stepPending stepState = iota // not reached yet
	stepActive                   // running now (shows the spinner)
	stepDone                     // completed (or completed in a prior resume)
	stepFailed                   // the ticket gave up while on this step
)

// phaseStep is one row of the pipeline stepper. The order of phaseSteps() is the
// canonical pipeline order; keys match the strings the pipeline passes to
// PhaseStart.
type phaseStep struct {
	key   string
	label string
	state stepState
	tag   string        // model/effort recovered from the phase's agent_call
	took  time.Duration // wall-clock once the step leaves "active"
	start time.Time
	subs  []string // self-heal sub-steps shown nested under the active phase
}

// phaseSteps returns a fresh, all-pending stepper in pipeline order. Build →
// handoff → verify map 1:1 to phase functions; commit/pr and ci/merge are the two
// halves of CommitAndPR and CIAndMerge, surfaced separately so progress reads
// finer than the five state checkpoints.
func phaseSteps() []phaseStep {
	return []phaseStep{
		{key: "build", label: "Build"},
		{key: "handoff", label: "Handoff"},
		{key: "verify", label: "Verify"},
		{key: "commit", label: "Commit"},
		{key: "pr", label: "PR"},
		{key: "ci", label: "CI"},
		{key: "merge", label: "Merge"},
	}
}

// stepIndex returns the position of key in steps, or -1.
func stepIndex(steps []phaseStep, key string) int {
	for i := range steps {
		if steps[i].key == key {
			return i
		}
	}
	return -1
}

// glyph returns the leading marker for a step state.
func (s stepState) glyph() string {
	switch s {
	case stepDone:
		return "✓"
	case stepActive:
		return ""
	case stepFailed:
		return "✗"
	default:
		return "○"
	}
}

// renderStepper draws the vertical pipeline stepper. spinFrame is the current
// spinner frame, shown against the active step so liveness is obvious even while
// a phase blocks for minutes. width is the pane's text area; the dimmed detail
// (elapsed + model tag) is truncated to fit so it never wraps in the narrow
// column.
func (m model) renderStepper(spinFrame string, width int) string {
	var b strings.Builder
	for i := range m.steps {
		st := m.steps[i]
		marker := st.state.glyph()
		style := m.styles.StepPending
		switch st.state {
		case stepDone:
			style = m.styles.StepDone
		case stepActive:
			style = m.styles.StepActive
			marker = strings.TrimRight(spinFrame, " ")
		case stepFailed:
			style = m.styles.StepFailed
		}

		head := marker + " " + st.label
		line := style.Render(head)

		// Trailing detail: elapsed + model tag, dimmed and clipped to the room left
		// after the head plus a two-space gap.
		var detail []string
		if st.state == stepActive && !st.start.IsZero() {
			detail = append(detail, fmtDur(time.Since(st.start)))
		} else if st.took > 0 {
			detail = append(detail, fmtDur(st.took))
		}
		if st.tag != "" {
			detail = append(detail, st.tag)
		}
		if len(detail) > 0 {
			budget := width - lipgloss.Width(head) - 2
			if budget >= 4 {
				line += "  " + m.styles.StepTag.Render(truncate(strings.Join(detail, " · "), budget))
			}
		}

		b.WriteString(line)
		for _, sub := range st.subs {
			b.WriteString("\n" + m.styles.StepTag.Render("   ↳ "+truncate(sub, width-5)))
		}
		if i < len(m.steps)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// activeIndex returns the index of the currently-active step, or -1.
func activeIndex(steps []phaseStep) int {
	for i := range steps {
		if steps[i].state == stepActive {
			return i
		}
	}
	return -1
}

// startPhase marks key active: it closes out the previously-active step and any
// still-pending earlier steps (completed in a prior resume) as done, then lights
// up key. now is the moment the phase began.
func startPhase(steps []phaseStep, key string, now time.Time) []phaseStep {
	idx := stepIndex(steps, key)
	if idx < 0 {
		return steps
	}
	for i := range steps {
		switch {
		case i < idx && steps[i].state == stepPending:
			steps[i].state = stepDone
		case i < idx && steps[i].state == stepActive:
			steps[i].state = stepDone
			steps[i].took = now.Sub(steps[i].start)
		}
	}
	steps[idx].state = stepActive
	steps[idx].start = now
	return steps
}

// finalize closes the stepper when a ticket reaches a terminal state. ok marks
// the active step done; a quarantine marks it failed. now stamps the elapsed.
func finalize(steps []phaseStep, ok bool, now time.Time) []phaseStep {
	idx := activeIndex(steps)
	if idx < 0 {
		return steps
	}
	if ok {
		steps[idx].state = stepDone
	} else {
		steps[idx].state = stepFailed
	}
	steps[idx].took = now.Sub(steps[idx].start)
	return steps
}

// completedFraction is the share of steps that finished (done), for the progress
// bar. A failed step does not count toward completion.
func completedFraction(steps []phaseStep) float64 {
	if len(steps) == 0 {
		return 0
	}
	done := 0
	for i := range steps {
		if steps[i].state == stepDone {
			done++
		}
	}
	return float64(done) / float64(len(steps))
}
