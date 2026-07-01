package pipeline

import (
	"context"
	"errors"
	"testing"
)

func TestCleanup(t *testing.T) {
	cases := []struct {
		name      string
		enabled   bool
		agentErr  error
		wantCalls int
		wantPause bool
	}{
		{name: "disabled skips the agent", enabled: false, wantCalls: 0},
		{name: "enabled runs the agent once", enabled: true, agentErr: nil, wantCalls: 1},
		{name: "ordinary agent error fails open", enabled: true, agentErr: errors.New("boom"), wantCalls: 1},
		{name: "provider pause propagates", enabled: true, agentErr: errors.New("kimi run (cleanup): 429 usage limit reached"), wantCalls: 1, wantPause: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &countingRunner{results: []error{tc.agentErr}, name: "claude"}
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.Cleanup = tc.enabled

			err := p.cleanup(context.Background(), "COD-635")

			switch {
			case tc.wantPause && !IsPaused(err):
				t.Fatalf("cleanup err = %v, want a paused error", err)
			case !tc.wantPause && err != nil:
				t.Fatalf("cleanup err = %v, want nil (fails open)", err)
			}
			if runner.calls != tc.wantCalls {
				t.Errorf("agent calls = %d, want %d", runner.calls, tc.wantCalls)
			}
		})
	}
}
