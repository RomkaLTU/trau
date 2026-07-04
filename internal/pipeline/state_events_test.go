package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

// TestLifecycleEmitsStateChange is the source-of-truth guard for the dashboard
// recap and browser notifications: every terminal or blameless-stop transition
// must land one durable state_change event carrying the ticket, the state, and a
// reason that tells a usage-window pause apart from a re-auth pause.
func TestLifecycleEmitsStateChange(t *testing.T) {
	rate := errors.New("kimi run (verify): 429 usage limit reached")
	reauth := errors.New("claude run (build): Please run /login")

	cases := []struct {
		name       string
		wantState  string
		wantReason string
		run        func(t *testing.T, p *Pipeline)
	}{
		{
			name: "merge", wantState: "merged", wantReason: "",
			run: func(t *testing.T, p *Pipeline) {
				if err := p.markDone(context.Background(), "COD-1", "  ✓ merged %s"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "quarantine", wantState: "quarantined", wantReason: "bugfix budget exhausted",
			run: func(t *testing.T, p *Pipeline) {
				_ = p.giveUp(context.Background(), "COD-2", "bugfix budget exhausted")
			},
		},
		{
			name: "fault", wantState: "faulted", wantReason: "build",
			run: func(t *testing.T, p *Pipeline) {
				_ = p.State.Set("COD-3", "PHASE", state.Building)
				_ = p.fault(context.Background(), "COD-3", errors.New("process exited unexpectedly"))
			},
		},
		{
			name: "pause-usage", wantState: "paused", wantReason: "usage_window",
			run: func(t *testing.T, p *Pipeline) {
				_ = p.pause("COD-4", state.Verified, rate)
			},
		},
		{
			name: "pause-reauth", wantState: "paused", wantReason: "reauth",
			run: func(t *testing.T, p *Pipeline) {
				_ = p.pauseAuth("COD-5", state.Building, reauth)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.Remote = "origin"
			p.Events = event.New(&buf)

			tc.run(t, p)

			evs := stateChangeEvents(t, &buf)
			if len(evs) != 1 {
				t.Fatalf("emitted %d state_change events, want exactly 1: %v", len(evs), evs)
			}
			ev := evs[0]
			if got := strField(ev.Fields, "state"); got != tc.wantState {
				t.Errorf("state = %q, want %q", got, tc.wantState)
			}
			if got := strField(ev.Fields, "reason"); got != tc.wantReason {
				t.Errorf("reason = %q, want %q", got, tc.wantReason)
			}
			if got := strField(ev.Fields, "ticket"); got == "" {
				t.Errorf("ticket field empty, want the ticket id")
			}
		})
	}
}

func stateChangeEvents(t *testing.T, buf *bytes.Buffer) []event.Event {
	t.Helper()
	var out []event.Event
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev event.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		if ev.Kind == "state_change" {
			out = append(out, ev)
		}
	}
	return out
}

func strField(fields map[string]any, key string) string {
	if s, ok := fields[key].(string); ok {
		return s
	}
	return ""
}
