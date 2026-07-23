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

// TestMechanicalPhase pins which phases are mechanical (tracker-free, MCP-strippable).
// The five mechanical prefixes and their dynamic suffixes match; the tracker-reading
// phases — build/handoff/verify and pick — must not, or stripping MCP would break the
// MCP ticket-read fallback (build/handoff/verify) or ticket selection (pick).
func TestMechanicalPhase(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"cleanup", true},
		{"commit", true},
		{"repair1", true},
		{"bugfix2", true},
		{"push-repair1", true},
		{"build", false},
		{"handoff", false},
		{"verify", false},
		{"verify-retry2", false},
		{"lintfix", false},
		{"pick", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := MechanicalPhase(tc.label); got != tc.want {
			t.Errorf("MechanicalPhase(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// TestSteerablePhase pins which phases take operator steer notes. Repair and
// bugfix qualify even though MechanicalPhase calls them mechanical.
func TestSteerablePhase(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"build", true},
		{"handoff", true},
		{"verify", true},
		{"verify-retry2", true},
		{"verify-codex", true},
		{"repair1", true},
		{"bugfix2", true},
		{"commit", false},
		{"cleanup", false},
		{"lintfix", false},
		{"push-repair1", false},
		{"pick", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := SteerablePhase(tc.label); got != tc.want {
			t.Errorf("SteerablePhase(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}
