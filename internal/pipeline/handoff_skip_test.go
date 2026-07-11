package pipeline

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// recordingRunner dispatches by phase, counts calls, and remembers the last prompt it
// was handed per phase. When a verdict path is set it drops a passing verdict on the
// verify phase so the attempt is graded pass without a real agent.
type recordingRunner struct {
	mu         sync.Mutex
	calls      map[string]int
	prompts    map[string]string
	verifyPath string
}

func (r *recordingRunner) Run(ctx context.Context, prompt, phase string) (agent.Result, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
		r.prompts = map[string]string{}
	}
	r.calls[phase]++
	r.prompts[phase] = prompt
	r.mu.Unlock()
	if phase == "verify" && r.verifyPath != "" {
		if err := os.WriteFile(r.verifyPath, []byte(`{"pass":true,"summary":"ok","failures":[]}`), 0o644); err != nil {
			return agent.Result{}, err
		}
	}
	return agent.Result{Final: phase + "-ok"}, nil
}

func (r *recordingRunner) count(phase string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[phase]
}

func (r *recordingRunner) prompt(phase string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prompts[phase]
}

// detailTracker adds the IssueDetailer capability to the shared fakeTracker so the
// derive-mode verify path can inject real ticket content.
type detailTracker struct {
	*fakeTracker
	detail tracker.IssueDetail
}

func (t detailTracker) IssueDetail(context.Context, string) (tracker.IssueDetail, error) {
	return t.detail, nil
}

// TestVerifyTailModes pins both verify prompt shapes: with a brief it grades against
// the brief file; with none it derives its own checklist from the ticket and diff and
// carries the injected ticket content. The verdict schema is identical either way.
func TestVerifyTailModes(t *testing.T) {
	const schema = `"pass": true|false`

	brief := verifyTail("COD-1", handoffPath("COD-1"), verifyPath("COD-1"), "", "", "", "", "")
	if !strings.Contains(brief, "the QA brief at "+handoffPath("COD-1")) {
		t.Errorf("brief-mode prompt does not point at the brief:\n%s", brief)
	}
	if !strings.Contains(brief, "For each behavior the brief lists") {
		t.Errorf("brief-mode prompt lost its per-behavior instruction:\n%s", brief)
	}
	if !strings.Contains(brief, schema) {
		t.Errorf("brief-mode prompt lost the verdict schema:\n%s", brief)
	}

	ticketCtx := ticketContextNote("COD-1", tracker.IssueDetail{Title: "Tiny slice", Description: "Do the small thing."})
	derive := verifyTail("COD-1", "", verifyPath("COD-1"), "", "", "", "", ticketCtx)
	if strings.Contains(derive, "QA brief at") || strings.Contains(derive, "/tmp/handoff") {
		t.Errorf("derive-mode prompt dangles a brief reference:\n%s", derive)
	}
	if !strings.Contains(derive, "derive the concrete, checkable behaviors yourself") {
		t.Errorf("derive-mode prompt does not ask the verifier to derive its checklist:\n%s", derive)
	}
	if !strings.Contains(derive, "Tiny slice") {
		t.Errorf("derive-mode prompt did not carry the injected ticket content:\n%s", derive)
	}
	if !strings.Contains(derive, schema) {
		t.Errorf("derive-mode prompt changed the verdict schema:\n%s", derive)
	}
}

// TestRepairBugfixInstructionModes covers the graceful-degradation contract: repair
// and bugfix prompts reference the brief when one exists and drop the reference (in
// favor of the injected ticket content) when it does not.
func TestRepairBugfixInstructionModes(t *testing.T) {
	ticketCtx := ticketContextNote("COD-1", tracker.IssueDetail{Title: "Tiny slice", Description: "Do the small thing."})
	for _, tc := range []struct {
		name  string
		build func(handoff, ticketCtx string) string
	}{
		{name: "repair", build: func(h, tc string) string {
			return repairInstruction("COD-1", verifyPath("COD-1"), h, "feature/x", "boom", "", "", "", tc)
		}},
		{name: "bugfix", build: func(h, tc string) string {
			return bugfixInstruction("COD-1", verifyPath("COD-1"), h, "feature/x", "boom", "", "", "", tc)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withBrief := tc.build(handoffPath("COD-1"), "")
			if !strings.Contains(withBrief, "QA brief: "+handoffPath("COD-1")) {
				t.Errorf("brief-mode %s dropped the brief reference:\n%s", tc.name, withBrief)
			}

			noBrief := tc.build("", ticketCtx)
			if strings.Contains(noBrief, "QA brief:") || strings.Contains(noBrief, "/tmp/handoff") {
				t.Errorf("missing-brief %s dangles a brief reference:\n%s", tc.name, noBrief)
			}
			if !strings.Contains(noBrief, "Tiny slice") {
				t.Errorf("missing-brief %s did not carry the injected ticket content:\n%s", tc.name, noBrief)
			}
		})
	}
}

// TestVerifyTinyDiffDerivesChecklist is the end-to-end guard for the tiny-diff handoff
// skip: with no brief on disk and a tiny working tree, Verify runs no standalone
// handoff agent, grades in derive mode against the injected ticket content, and still
// reaches the verified checkpoint on a passing verdict.
func TestVerifyTinyDiffDerivesChecklist(t *testing.T) {
	id := "COD-79901"
	t.Cleanup(func() {
		_ = os.Remove(handoffPath(id))
		_ = os.Remove(verifyPath(id))
		_ = os.Remove(rubricPath(id))
	})

	r := &recordingRunner{verifyPath: verifyPath(id)}
	tr := detailTracker{fakeTracker: &fakeTracker{}, detail: tracker.IssueDetail{Title: "Tiny slice", Description: "Do the small thing."}}
	p := newTestPipeline(t, r, tr)
	p.Git = sizeGit{files: 2, lines: 20}

	if err := p.Verify(context.Background(), id); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if got := r.count("handoff"); got != 0 {
		t.Errorf("handoff agent ran %d times for a tiny diff, want 0", got)
	}
	if got := r.count("verify"); got != 1 {
		t.Errorf("verify ran %d times, want 1", got)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Verified {
		t.Errorf("PHASE = %q, want %q", got, state.Verified)
	}
	vp := r.prompt("verify")
	if strings.Contains(vp, "QA brief at") {
		t.Errorf("verify graded against a brief in derive mode:\n%s", vp)
	}
	if !strings.Contains(vp, "derive the concrete, checkable behaviors yourself") {
		t.Errorf("verify was not told to derive its own checklist:\n%s", vp)
	}
	if !strings.Contains(vp, "Tiny slice") {
		t.Errorf("verify prompt did not carry the injected ticket content:\n%s", vp)
	}
}
