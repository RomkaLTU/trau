package tui

import (
	"strings"
	"testing"
	"time"
)

// feedText for rows not tied to a step (stepIdx < 0 or out of range) must return
// the parsed text untouched — that covers every ✓/✗/→/↳/=== row.
func TestFeedTextFallsBackToParsedText(t *testing.T) {
	m := model{steps: phaseSteps()}
	for _, idx := range []int{-1, 99} {
		if got := m.feedText(feedEntry{text: "claude", stepIdx: idx}); got != "claude" {
			t.Fatalf("stepIdx %d: got %q, want %q", idx, got, "claude")
		}
	}
}

// A completed phase composes "<model tag> · <frozen duration>" from its step.
func TestFeedTextCompletedComposesTagAndDuration(t *testing.T) {
	m := model{steps: phaseSteps()}
	m.steps[0].state = stepDone
	m.steps[0].tag = "opus-4-8 @high"
	m.steps[0].took = 5*time.Minute + 3*time.Second

	if got, want := m.feedText(feedEntry{text: "claude", stepIdx: 0}), "opus-4-8 @high · 5m03s"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An active phase with no tag yet keeps the parsed tag and ticks live elapsed.
func TestFeedTextActiveUsesLiveElapsed(t *testing.T) {
	m := model{steps: phaseSteps()}
	m.steps[2].state = stepActive
	m.steps[2].start = time.Now().Add(-90 * time.Second)

	got := m.feedText(feedEntry{text: "claude", stepIdx: 2})
	if !strings.HasPrefix(got, "claude · 1m3") {
		t.Fatalf("got %q, want prefix %q", got, "claude · 1m3")
	}
}
