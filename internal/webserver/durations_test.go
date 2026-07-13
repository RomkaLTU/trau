package webserver

import (
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tokens"
)

func at(base time.Time, secs int) time.Time { return base.Add(time.Duration(secs) * time.Second) }

func durationOf(durs []StepDuration, step string) (int64, bool) {
	for _, d := range durs {
		if d.Step == step {
			return d.DurationMS, true
		}
	}
	return 0, false
}

// TestStepDurations pins the read-time derivation of ADR 0009: an Activity runs
// until the next activity_change or, for the last, until the run's terminal
// state_change; the spans group into Build/Verify/Ship. CI wait lands in Ship, a
// run still in flight leaves its last Activity open, and a resumed run measures only
// the latest segment so the totals track the displayed wall-clock.
func TestStepDurations(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		starts    []activityStart
		terminals []time.Time
		want      []StepDuration
	}{
		{
			name:   "no activity change events yields no durations",
			starts: nil,
			want:   nil,
		},
		{
			name: "whole run sums to wall-clock across the three steps",
			starts: []activityStart{
				{at(base, 0), activity.Build},
				{at(base, 60), activity.Verify},
				{at(base, 120), activity.Commit},
			},
			terminals: []time.Time{at(base, 180)},
			want: []StepDuration{
				{Step: "Build", DurationMS: 60_000},
				{Step: "Verify", DurationMS: 60_000},
				{Step: "Ship", DurationMS: 60_000},
			},
		},
		{
			name: "ci wait is attributed to Ship",
			starts: []activityStart{
				{at(base, 0), activity.Build},
				{at(base, 60), activity.Commit},
				{at(base, 70), activity.CIWait},
				{at(base, 130), activity.Merge},
			},
			terminals: []time.Time{at(base, 135)},
			want: []StepDuration{
				{Step: "Build", DurationMS: 60_000},
				{Step: "Ship", DurationMS: 75_000},
			},
		},
		{
			name: "run still in flight leaves the last activity open",
			starts: []activityStart{
				{at(base, 0), activity.Build},
				{at(base, 60), activity.Verify},
			},
			want: []StepDuration{
				{Step: "Build", DurationMS: 60_000},
			},
		},
		{
			name: "a resumed run measures only the latest segment",
			starts: []activityStart{
				{at(base, 0), activity.Build},
				{at(base, 30), activity.Verify},
				{at(base, 100), activity.Build},
				{at(base, 160), activity.Verify},
			},
			terminals: []time.Time{at(base, 40), at(base, 220)},
			want: []StepDuration{
				{Step: "Build", DurationMS: 60_000},
				{Step: "Verify", DurationMS: 60_000},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stepDurations(tt.starts, tt.terminals)
			if len(got) != len(tt.want) {
				t.Fatalf("durations = %+v, want %+v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Fatalf("durations = %+v, want %+v", got, tt.want)
				}
			}
		})
	}
}

// TestRunDetailDerivesStepDurations covers the wired path end to end: a completed
// run's activity_change/state_change events posted to the hub surface as per-Step
// durations on the run-detail resource, with CI wait folded into Ship and visible
// for the first time.
func TestRunDetailDerivesStepDurations(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-500", map[string]string{"PHASE": state.Merged})

	ts := instancesServer(t, home)
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	stamp := func(secs int) string { return at(base, secs).Format(time.RFC3339) }
	postEvents(t, ts, "acme",
		hubclient.Event{TS: stamp(0), Kind: "activity_change", Fields: `{"ticket":"COD-500","activity":"build"}`},
		hubclient.Event{TS: stamp(120), Kind: "activity_change", Fields: `{"ticket":"COD-500","activity":"verify"}`},
		hubclient.Event{TS: stamp(180), Kind: "activity_change", Fields: `{"ticket":"COD-500","activity":"commit"}`},
		hubclient.Event{TS: stamp(200), Kind: "activity_change", Fields: `{"ticket":"COD-500","activity":"ci-wait"}`},
		hubclient.Event{TS: stamp(320), Kind: "activity_change", Fields: `{"ticket":"COD-500","activity":"merge"}`},
		hubclient.Event{TS: stamp(330), Kind: "state_change", Fields: `{"ticket":"COD-500","state":"merged"}`},
	)

	d := getRunDetail(t, ts, "acme", "COD-500")

	build, ok := durationOf(d.Durations, "Build")
	if !ok || build != 120_000 {
		t.Errorf("Build duration = %d present=%v, want 120000", build, ok)
	}
	if verify, ok := durationOf(d.Durations, "Verify"); !ok || verify != 60_000 {
		t.Errorf("Verify duration = %d present=%v, want 60000", verify, ok)
	}
	// Ship = commit(20s) + ci-wait(120s) + merge(10s) — the CI wait now visible.
	if ship, ok := durationOf(d.Durations, "Ship"); !ok || ship != 150_000 {
		t.Errorf("Ship duration = %d present=%v, want 150000 (ci wait included)", ship, ok)
	}
}

// TestRunDetailNoDurationsBeforeSignal covers the compatibility floor: a run with no
// activity_change events — one predating ADR 0009 — carries no durations rather than
// a guess, while its cost table is unaffected.
func TestRunDetailNoDurationsBeforeSignal(t *testing.T) {
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	seedCheckpoint(t, runsDir, "COD-501", map[string]string{"PHASE": state.Merged})
	seedTokens(t, home, runsDir, "COD-501", []phaseCall{
		{"build", tokens.Record{Input: 100, Output: 50, Turns: 3}},
	})

	d := getRunDetail(t, instancesServer(t, home), "acme", "COD-501")
	if len(d.Durations) != 0 {
		t.Errorf("durations = %+v, want none for a run predating the signal", d.Durations)
	}
	if len(d.Costs) != 1 {
		t.Errorf("costs = %+v, want the build spend still recorded", d.Costs)
	}
}
