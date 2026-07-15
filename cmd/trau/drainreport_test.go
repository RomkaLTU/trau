package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/state"
)

// TestDrainClass pins the drain-report contract: only a nil exit error posts the
// empty class the hub reads as a clean finish. A generic error — a git preflight
// failure, a crashed child, anything outside the pause taxonomy — must post
// faulted, because an empty class settles the queue item done and marks every
// sub-issue of an epic done with it (the COD-896 incident: an epic that never
// ran settled done after its child died on a conflicted checkout).
func TestDrainClass(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass string
	}{
		{"nil error is a clean finish", nil, ""},
		{"paused stays paused", &pipeline.PausedError{ID: "COD-1", Phase: "build", Reason: "usage limit"}, state.FailPaused},
		{"wrapped paused stays paused", fmt.Errorf("loop: %w", &pipeline.PausedError{ID: "COD-1", Phase: "build", Reason: "usage limit"}), state.FailPaused},
		{"fault posts faulted", &pipeline.FaultError{ID: "COD-1", Phase: "verify", Err: errors.New("push failed")}, state.FailFaulted},
		{"generic error posts faulted, never clean", errors.New("repo has unmerged paths"), state.FailFaulted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, reason := drainClass(tt.err)
			if class != tt.wantClass {
				t.Fatalf("drainClass(%v) class = %q, want %q", tt.err, class, tt.wantClass)
			}
			if tt.err == nil && reason != "" {
				t.Fatalf("drainClass(nil) reason = %q, want empty", reason)
			}
			if tt.err != nil && reason != tt.err.Error() {
				t.Fatalf("drainClass(%v) reason = %q, want %q", tt.err, reason, tt.err.Error())
			}
		})
	}
}
