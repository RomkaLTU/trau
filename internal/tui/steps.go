package tui

import "time"

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
	key          string
	label        string
	state        stepState
	tag          string        // model/effort recovered from the phase's agent_call
	took         time.Duration // wall-clock once the step leaves "active"
	start        time.Time
	subs         []childSpan // self-heal attempts nested under the active phase
	transcript   string      // pty transcript this phase owns, tailed for its live span
	tailSnapshot []string    // last tail lines frozen when the phase failed
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

// doneSteps counts the phases that have finished, for the pane's "n/N" heading.
func doneSteps(steps []phaseStep) int {
	done := 0
	for i := range steps {
		if steps[i].state == stepDone {
			done++
		}
	}
	return done
}

// failedIndex returns the index of the phase that gave up, or -1.
func failedIndex(steps []phaseStep) int {
	for i := range steps {
		if steps[i].state == stepFailed {
			return i
		}
	}
	return -1
}
