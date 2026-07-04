package agent

import "testing"

// TestRouteKey pins the label→phase collapse, including the COD-641 additions
// (cleanup/lintfix) that used to fall through to pick. Dynamic suffixes
// ("verify-retry2") and unknown labels ("status") must still resolve as before,
// and the c-prefixed phases (cleanup/commit) must not shadow each other.
func TestRouteKey(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"build", PhaseBuild},
		{"handoff", PhaseHandoff},
		{"verify", PhaseVerify},
		{"verify-retry2", PhaseVerify},
		{"repair1", PhaseRepair},
		{"bugfix", PhaseBugfix},
		{"cleanup", PhaseCleanup},
		{"lintfix", PhaseLintfix},
		{"commit", PhaseCommit},
		{"plan", PhasePlan},
		{"slice", PhaseSlice},
		{"pick", PhasePick},
		{"status", PhasePick},
		{"", PhasePick},
	}
	for _, tc := range cases {
		if got := RouteKey(tc.label); got != tc.want {
			t.Errorf("RouteKey(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// TestPreambleFor pins the planning Preamble's scope: the plan-scoped variant
// covers both planning labels (plan and slice) and nothing else.
func TestPreambleFor(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"plan", "planning"},
		{"slice", "planning"},
		{"build", "base"},
		{"verify-retry2", "base"},
		{"", "base"},
	}
	for _, tc := range cases {
		if got := preambleFor("base", "planning", tc.label); got != tc.want {
			t.Errorf("preambleFor(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}
