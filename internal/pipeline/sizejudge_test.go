package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// sizeRunner stands in for the judge agent: it writes a canned verdict JSON to the
// verdict path (empty verdict = writes nothing, simulating an agent that produced
// none) and counts how many times it ran, so a disabled judge can be shown to make
// zero calls.
type sizeRunner struct {
	path    string
	verdict string
	calls   int
}

func (r *sizeRunner) Run(_ context.Context, _, _ string) (agent.Result, error) {
	r.calls++
	if r.verdict != "" && r.path != "" {
		_ = os.WriteFile(r.path, []byte(r.verdict), 0o644)
	}
	return agent.Result{}, nil
}

// sizeTracker adds the IssueDetailer/IssueLabeler capabilities the size guard needs
// on top of the shared fakeTracker, and records the label applied and the reason
// the ticket was quarantined with.
type sizeTracker struct {
	*fakeTracker
	detail           tracker.IssueDetail
	detailErr        error
	labels           []string
	quarantined      int
	quarantineReason string
}

func (t *sizeTracker) IssueDetail(context.Context, string) (tracker.IssueDetail, error) {
	return t.detail, t.detailErr
}

func (t *sizeTracker) AddLabel(_ context.Context, _, label string) error {
	t.labels = append(t.labels, label)
	return nil
}

func (t *sizeTracker) Quarantine(_ context.Context, _, reason string) error {
	t.quarantined++
	t.quarantineReason = reason
	return nil
}

// TestSizeGuard is the COD-632 pre-flight guard: a mocked judge verdict of
// fits_one_window=false must quarantine + apply the split label on an unattended
// run and warn-but-proceed on an attended one, while a fits verdict, no verdict, or
// a disabled judge all proceed to build — and a disabled judge makes zero calls.
func TestSizeGuard(t *testing.T) {
	const tooBig = `{"fits_one_window":false,"reason":"11 acceptance criteria across backend and frontend","suggested_slices":["backend endpoints","frontend queue"]}`
	cases := []struct {
		name           string
		enabled        bool
		attended       bool
		verdict        string
		wantErr        bool
		wantQuarantine bool
		wantLabel      bool
		wantJudgeCall  bool
	}{
		{name: "unattended too-large quarantines and labels", enabled: true, verdict: tooBig, wantErr: true, wantQuarantine: true, wantLabel: true, wantJudgeCall: true},
		{name: "attended too-large warns and proceeds", enabled: true, attended: true, verdict: tooBig, wantJudgeCall: true},
		{name: "fits proceeds", enabled: true, verdict: `{"fits_one_window":true,"reason":"","suggested_slices":[]}`, wantJudgeCall: true},
		{name: "no verdict fails open", enabled: true, verdict: "", wantJudgeCall: true},
		{name: "disabled skips the judge", enabled: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "COD-63200"
			t.Cleanup(func() { _ = os.Remove(sizeJudgePath(id)) })

			runner := &sizeRunner{path: sizeJudgePath(id), verdict: tc.verdict}
			tr := &sizeTracker{fakeTracker: &fakeTracker{}, detail: tracker.IssueDetail{Title: "big ticket", Description: "lots to do"}}
			p := newTestPipeline(t, runner, tr)
			p.SizeJudge = tc.enabled
			p.SplitLabel = "needs-split"
			p.Attended = tc.attended

			err := p.sizeGuard(context.Background(), id)

			if tc.wantErr {
				if !isGiveUp(err) {
					t.Fatalf("sizeGuard err = %v, want a *GiveUpError", err)
				}
			} else if err != nil {
				t.Fatalf("sizeGuard err = %v, want nil (proceed)", err)
			}

			if tc.wantJudgeCall && runner.calls != 1 {
				t.Errorf("judge calls = %d, want 1", runner.calls)
			}
			if !tc.wantJudgeCall && runner.calls != 0 {
				t.Errorf("judge calls = %d, want 0 (no added cost)", runner.calls)
			}

			if tc.wantQuarantine {
				if tr.quarantined != 1 {
					t.Errorf("Quarantine calls = %d, want 1", tr.quarantined)
				}
				if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
					t.Errorf("PHASE = %q, want quarantined", got)
				}
				if !strings.Contains(tr.quarantineReason, "suggested split") || !strings.Contains(tr.quarantineReason, "backend endpoints") {
					t.Errorf("quarantine reason = %q, want it to carry the suggested seams", tr.quarantineReason)
				}
			} else {
				if tr.quarantined != 0 {
					t.Errorf("Quarantine calls = %d, want 0", tr.quarantined)
				}
				if got := p.State.Get(id, "PHASE"); got == state.Quarantined {
					t.Errorf("PHASE = quarantined, want the ticket left buildable")
				}
			}

			if tc.wantLabel {
				if len(tr.labels) != 1 || tr.labels[0] != "needs-split" {
					t.Errorf("labels = %v, want [needs-split]", tr.labels)
				}
			} else if len(tr.labels) != 0 {
				t.Errorf("labels = %v, want none", tr.labels)
			}
		})
	}
}

// TestSizeGuardPersistsDurableVerdict covers the durable copy the post-run
// cost-anomaly flag (COD-644) keys off: a usable verdict is written to
// runs/<id>/sizejudge.json, surviving after /tmp is gone.
func TestSizeGuardPersistsDurableVerdict(t *testing.T) {
	id := "COD-63202"
	t.Cleanup(func() { _ = os.Remove(sizeJudgePath(id)) })

	runner := &sizeRunner{path: sizeJudgePath(id), verdict: `{"fits_one_window":true,"reason":"","suggested_slices":[]}`}
	tr := &sizeTracker{fakeTracker: &fakeTracker{}, detail: tracker.IssueDetail{Title: "small", Description: "little to do"}}
	p := newTestPipeline(t, runner, tr)
	p.SizeJudge = true

	if err := p.sizeGuard(context.Background(), id); err != nil {
		t.Fatalf("sizeGuard err = %v, want nil", err)
	}

	data, err := os.ReadFile(filepath.Join(p.RunsDir, id, "sizejudge.json"))
	if err != nil {
		t.Fatalf("durable verdict not written: %v", err)
	}
	if !strings.Contains(string(data), `"fits_one_window":true`) {
		t.Errorf("durable verdict = %s, want fits_one_window:true", data)
	}
}

// TestSizeGuardSkipsWithoutIssueDetailer covers graceful degradation: a tracker
// that cannot supply the ticket detail (no IssueDetailer) makes the guard skip
// entirely — no judge call, no quarantine — so the build proceeds unchanged.
func TestSizeGuardSkipsWithoutIssueDetailer(t *testing.T) {
	id := "COD-63201"
	runner := &sizeRunner{path: sizeJudgePath(id), verdict: `{"fits_one_window":false,"reason":"x","suggested_slices":["a"]}`}
	tr := &fakeTracker{}
	p := newTestPipeline(t, runner, tr)
	p.SizeJudge = true
	p.SplitLabel = "needs-split"

	if err := p.sizeGuard(context.Background(), id); err != nil {
		t.Fatalf("sizeGuard err = %v, want nil (skip and proceed)", err)
	}
	if runner.calls != 0 {
		t.Errorf("judge calls = %d, want 0 (skipped, no detail source)", runner.calls)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine calls = %d, want 0", tr.quarantineCalls)
	}
}
