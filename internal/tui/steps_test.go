package tui

import (
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
)

// advanceActivity lights up the Activity's Step and folds earlier ones, stamping
// the previous Step's elapsed as it hands off.
func TestAdvanceActivityFoldsEarlierSteps(t *testing.T) {
	base := time.Now()
	rows := advanceActivity(stepRows(), activity.Build, "", base)
	rows = advanceActivity(rows, activity.Verify, "", base.Add(time.Minute))

	if rows[0].state != stepDone {
		t.Errorf("Build should fold to done, got %v", rows[0].state)
	}
	if rows[0].took != time.Minute {
		t.Errorf("Build took = %v, want 1m", rows[0].took)
	}
	if got := activeIndex(rows); got != 1 {
		t.Errorf("active index = %d, want 1 (Verify)", got)
	}
}

// A sub-activity within the active Step updates the sub-label without restarting
// the Step timer — the whole verify→repair→bugfix loop measures as one Verify.
func TestAdvanceActivityKeepsStepTimerAcrossSubActivities(t *testing.T) {
	start := time.Now()
	rows := advanceActivity(stepRows(), activity.Verify, "", start)
	verifyStart := rows[1].start

	rows = advanceActivity(rows, activity.Repair, "repair1", start.Add(30*time.Second))
	if !rows[1].start.Equal(verifyStart) {
		t.Errorf("repair reset the Verify timer: start moved %v → %v", verifyStart, rows[1].start)
	}
	if rows[1].act != activity.Repair || rows[1].detail != "repair1" {
		t.Errorf("sub-label not updated: act=%q detail=%q", rows[1].act, rows[1].detail)
	}
	if activeIndex(rows) != 1 {
		t.Error("Verify should stay the active Step through a repair")
	}
}

// A stale Activity for an already-finished Step never re-lights it.
func TestAdvanceActivityIgnoresBackwardActivity(t *testing.T) {
	now := time.Now()
	rows := advanceActivity(stepRows(), activity.Verify, "", now) // Build done, Verify active
	rows = advanceActivity(rows, activity.Handoff, "", now)       // Build-step activity, stale

	if rows[0].state != stepDone {
		t.Errorf("Build should stay done, got %v", rows[0].state)
	}
	if got := activeIndex(rows); got != 1 {
		t.Errorf("Verify should stay active, got index %d", got)
	}
}
