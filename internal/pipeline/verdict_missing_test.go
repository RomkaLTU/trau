package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// verdictFileRunner stands in for a verify agent that honors, breaks, or never
// reaches the verdict-file contract: it writes body verbatim (nothing when empty),
// then returns err.
type verdictFileRunner struct {
	path string
	body string
	err  error
}

func (r verdictFileRunner) Run(context.Context, string, string) (agent.Result, error) {
	if r.body != "" {
		_ = os.WriteFile(r.path, []byte(r.body), 0o644)
	}
	return agent.Result{}, r.err
}

// TestVerifyAttemptEmitsVerdictMissing covers the contract-miss counter: an
// attempt that ends with no usable verdict emits exactly one verdict_missing event
// naming the cause, so the mid-cycle misses the hub artifact overwrites stay
// measurable. An agent that honored the contract stays silent.
func TestVerifyAttemptEmitsVerdictMissing(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		err    error
		reason string
	}{
		{"no verdict file", "", nil, "missing"},
		{"unparseable verdict", "{not json", nil, "unparseable"},
		{"agent error", "", errors.New("agent crashed: boom"), "agent-error"},
		{"verdict written", `{"pass":true,"summary":"ok"}`, nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "COD-92001"
			t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })
			var buf bytes.Buffer
			p := newTestPipeline(t, verdictFileRunner{path: verifyPath(id), body: tc.body, err: tc.err}, &fakeTracker{})
			p.Events = event.New(&buf)

			if _, err := p.verifyAttempt(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "", ""); err != nil {
				t.Fatalf("verifyAttempt: %v", err)
			}

			evs := kindEvents(t, &buf, event.KindVerdictMissing)
			if tc.reason == "" {
				if len(evs) != 0 {
					t.Fatalf("emitted %d verdict_missing events, want 0", len(evs))
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("emitted %d verdict_missing events, want exactly 1", len(evs))
			}
			ev := evs[0]
			if got := strField(ev.Fields, "reason"); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
			if got := strField(ev.Fields, "ticket"); got != id {
				t.Errorf("ticket = %q, want %q", got, id)
			}
			if ev.Phase != "verify" {
				t.Errorf("phase = %q, want %q", ev.Phase, "verify")
			}
			if member, ok := ev.Fields["member"]; ok {
				t.Errorf("single verifier carried member = %v, want no member field", member)
			}
		})
	}
}

// TestRunPanelAttributesVerdictMissingByMember keeps the panel attribution honest:
// only the member that skipped its verdict file is counted, by name and under its
// own phase label.
func TestRunPanelAttributesVerdictMissingByMember(t *testing.T) {
	const id = "COD-92002"
	mu := &sync.Mutex{}
	labels := &[]string{}
	panel := []Verifier{
		{Name: "alpha", Runner: panelRunner{
			id: id, name: "alpha", write: true,
			v:  verdict{Pass: true, Summary: "alpha ok"},
			mu: mu, labels: labels,
		}},
		{Name: "beta", Runner: panelRunner{id: id, name: "beta", mu: mu, labels: labels}},
	}
	var buf bytes.Buffer
	p := newPanelPipeline(t, panel, false)
	p.Events = event.New(&buf)

	if _, err := p.runPanel(context.Background(), id, "verify", "brief", "", "", "", "", "", "", "", "", false); err != nil {
		t.Fatalf("runPanel: %v", err)
	}

	evs := kindEvents(t, &buf, event.KindVerdictMissing)
	if len(evs) != 1 {
		t.Fatalf("emitted %d verdict_missing events, want exactly 1 (only beta missed)", len(evs))
	}
	ev := evs[0]
	if got := strField(ev.Fields, "member"); got != "beta" {
		t.Errorf("member = %q, want %q", got, "beta")
	}
	if got := strField(ev.Fields, "reason"); got != "missing" {
		t.Errorf("reason = %q, want %q", got, "missing")
	}
	if ev.Phase != "verify-beta" {
		t.Errorf("phase = %q, want %q", ev.Phase, "verify-beta")
	}
}
