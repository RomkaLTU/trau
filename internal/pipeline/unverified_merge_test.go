package pipeline

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/state"
)

// A verdict the verifier could not fully settle still ships its PR — the work is
// preserved and reviewable — but the merge is a human's: CIAndMerge routes to the
// manual-merge wait despite AUTO_MERGE=1, flags the run with a durable event, and
// the PR body names the criterion nobody checked.
func TestUnverifiedCriterionShipsThePRButBlocksAutoMerge(t *testing.T) {
	id := "COD-91482"
	gh := &waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "MERGED"}}}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	p.AutoMerge = true
	var buf bytes.Buffer
	p.Events = event.New(&buf)
	writeSliceVerdict(t, id, verdict{
		Pass:    true,
		Summary: "totals compute correctly",
		Browser: "not-applicable",
		Criteria: []criterionResult{
			{Text: "totals shown", Status: criterionSatisfied},
			{Text: "cart empties on checkout", Status: criterionUnverified, Note: "no automation browser reachable"},
		},
	})
	seedPROpen(t, p, id, "82", "feature/COD-91482-x")

	body := p.prBody(context.Background(), id, "")
	if !strings.Contains(body, "## Unverified criteria") {
		t.Errorf("PR body missing the unverified criteria section:\n%s", body)
	}
	if !strings.Contains(body, "- cart empties on checkout — no automation browser reachable") {
		t.Errorf("PR body must name the unverified criterion and its note:\n%s", body)
	}
	if strings.Contains(body, "- totals shown\n") {
		t.Errorf("PR body lists a settled criterion as unverified:\n%s", body)
	}

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 (an unverified criterion is a human's call)", gh.mergeCalls)
	}
	if evs := awaitingMergeEvents(t, &buf); len(evs) != 1 {
		t.Errorf("emitted %d awaiting_merge events, want exactly 1 (the manual-merge wait was entered)", len(evs))
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged once the human merged it", got)
	}

	evs := kindEvents(t, &buf, event.KindCriteriaUnverified)
	if len(evs) != 1 {
		t.Fatalf("emitted %d criteria_unverified events, want exactly 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != id {
		t.Errorf("ticket field = %q, want %q", got, id)
	}
	if !strings.Contains(evs[0].Msg, "cart empties on checkout") {
		t.Errorf("event msg = %q, want it to name the unverified criterion", evs[0].Msg)
	}
}

// A verdict that settled every criterion is untouched by the hold: green CI
// auto-merges exactly as before, and the PR body carries no unverified section.
func TestFullySatisfiedVerdictStillAutoMerges(t *testing.T) {
	id := "COD-91483"
	git := &mergeGit{branch: "feature/COD-91483-x"}
	gh := &mergeGitHub{}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	writeSliceVerdict(t, id, verdict{
		Pass:     true,
		Summary:  "totals compute correctly",
		Browser:  "not-applicable",
		Criteria: []criterionResult{{Text: "totals shown", Status: criterionSatisfied}},
	})
	seedPROpen(t, p, id, "83", git.branch)

	if body := p.prBody(context.Background(), id, ""); strings.Contains(body, "Unverified criteria") {
		t.Errorf("a fully satisfied verdict must ship no unverified section:\n%s", body)
	}
	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("Merge called %d times, want 1", gh.mergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged", got)
	}
}
