package activity

import "testing"

// StepOf pins every Activity to its display Step — the same grouping the web
// stepper carries in web/src/lib/steps.ts, so the two surfaces cannot drift.
func TestStepOf(t *testing.T) {
	cases := map[Activity]Step{
		Build:     StepBuild,
		LintFix:   StepBuild,
		Cleanup:   StepBuild,
		Handoff:   StepBuild,
		Verify:    StepVerify,
		Repair:    StepVerify,
		Bugfix:    StepVerify,
		Commit:    StepShip,
		PR:        StepShip,
		CIWait:    StepShip,
		Merge:     StepShip,
		MergeWait: StepShip,
	}
	for act, want := range cases {
		if got := StepOf(act); got != want {
			t.Errorf("StepOf(%q) = %q, want %q", act, got, want)
		}
	}
}

func TestStepsOrder(t *testing.T) {
	want := []Step{StepBuild, StepVerify, StepShip}
	if len(Steps) != len(want) {
		t.Fatalf("Steps = %v, want %v", Steps, want)
	}
	for i := range want {
		if Steps[i] != want[i] {
			t.Errorf("Steps[%d] = %q, want %q", i, Steps[i], want[i])
		}
	}
}
