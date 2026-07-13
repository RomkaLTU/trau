package tui

import (
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
)

// stepState is the lifecycle of one Step as the stepper sees it.
type stepState int

const (
	stepPending stepState = iota // not reached yet
	stepActive                   // running now (shows the spinner)
	stepDone                     // completed (or completed in a prior resume)
	stepFailed                   // the ticket gave up while on this step
)

// stepRow is one row of the three-Step stepper (Build → Verify → Ship, ADR 0009).
// The present-tense Activity within the active Step drives the sub-label; an
// Activity change that stays inside the same Step updates only the sub-label,
// leaving took running so re-verify/repair/bugfix never reset the Step timer.
type stepRow struct {
	step         activity.Step
	state        stepState
	act          activity.Activity // present-tense Activity within this Step
	detail       string            // raw call label behind act, e.g. "repair2"
	tag          string            // model/effort recovered from the step's agent_call
	took         time.Duration     // wall-clock once the step leaves "active"
	start        time.Time
	transcript   string   // pty transcript this step owns, tailed for its live span
	tailSnapshot []string // last tail lines frozen when the step failed
}

// stepRows returns a fresh, all-pending stepper in canonical Step order.
func stepRows() []stepRow {
	rows := make([]stepRow, len(activity.Steps))
	for i, s := range activity.Steps {
		rows[i] = stepRow{step: s}
	}
	return rows
}

// stepIndexOf returns the position of step s, or -1.
func stepIndexOf(rows []stepRow, s activity.Step) int {
	for i := range rows {
		if rows[i].step == s {
			return i
		}
	}
	return -1
}

// activeIndex returns the index of the currently-active step, or -1.
func activeIndex(rows []stepRow) int {
	for i := range rows {
		if rows[i].state == stepActive {
			return i
		}
	}
	return -1
}

// advanceActivity records the present-tense Activity on the stepper: it lights up
// the Activity's Step — closing out any earlier steps as done and stamping the
// previous step's elapsed — then stores act+detail for the sub-label. An Activity
// that stays within the already-active Step updates only the sub-label, so the Step
// timer keeps running across re-verify/repair/bugfix. A stale Activity for an
// already-finished Step is ignored.
func advanceActivity(rows []stepRow, act activity.Activity, detail string, now time.Time) []stepRow {
	idx := stepIndexOf(rows, activity.StepOf(act))
	if idx < 0 {
		return rows
	}
	cur := activeIndex(rows)
	if cur >= 0 && idx < cur {
		return rows
	}
	if idx != cur {
		for i := 0; i < idx; i++ {
			if rows[i].state == stepActive {
				rows[i].took = now.Sub(rows[i].start)
			}
			rows[i].state = stepDone
		}
		rows[idx].state = stepActive
		rows[idx].start = now
	}
	rows[idx].act = act
	rows[idx].detail = detail
	return rows
}

// finalize closes the stepper when a ticket reaches a terminal state. ok marks the
// active step done; a quarantine marks it failed. now stamps the elapsed.
func finalize(rows []stepRow, ok bool, now time.Time) []stepRow {
	idx := activeIndex(rows)
	if idx < 0 {
		return rows
	}
	if ok {
		rows[idx].state = stepDone
	} else {
		rows[idx].state = stepFailed
	}
	rows[idx].took = now.Sub(rows[idx].start)
	return rows
}

// doneSteps counts the Steps that have finished, for the pane's "n/N" heading.
func doneSteps(rows []stepRow) int {
	done := 0
	for i := range rows {
		if rows[i].state == stepDone {
			done++
		}
	}
	return done
}

// failedIndex returns the index of the Step that gave up, or -1.
func failedIndex(rows []stepRow) int {
	for i := range rows {
		if rows[i].state == stepFailed {
			return i
		}
	}
	return -1
}
