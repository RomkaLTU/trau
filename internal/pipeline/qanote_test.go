package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// qaTracker is a fakeTracker that can also post QA notes, recording every one so a
// run's comment count and content are observable.
type qaTracker struct {
	*fakeTracker
	notes   []tracker.QANote
	postErr error
}

func newQATracker() *qaTracker { return &qaTracker{fakeTracker: &fakeTracker{}} }

func (t *qaTracker) PostQANote(_ context.Context, _ string, note tracker.QANote) error {
	t.notes = append(t.notes, note)
	return t.postErr
}

const qaNotePR = "https://github.test/pr/91"

// newQADeliveryPipeline is a pipeline whose CommitAndPR reaches a real PR URL, with
// a passing verdict on disk for the report to state.
func newQADeliveryPipeline(t *testing.T, tr tracker.Tracker, id string) *Pipeline {
	t.Helper()
	git := &localGit{hasRemote: true, branch: "feature/" + id + "-slice"}
	gh := &countingGitHub{epicGitHub: epicGitHub{createURL: qaNotePR}}
	p := localTestPipeline(t, git, gh, tr)
	p.QANotes = true
	p.AppURL = "http://app.test"
	writeSliceVerdict(t, id, verdict{
		Pass:    true,
		Summary: "the panel renders and the totals add up",
		Checks:  []checkResult{{Name: "tests", Pass: true}, {Name: "lint", Pass: true}},
		Browser: "driven",
	})
	return p
}

// A delivered slice leaves exactly one comment on its ticket, stating the same
// verify facts the PR body does plus the PR the run opened.
func TestDeliveryPostsOneQANoteWithTheVerifyFactsAndPRLink(t *testing.T) {
	id := "COD-91426"
	tr := newQATracker()
	p := newQADeliveryPipeline(t, tr, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	body := tr.notes[0].Body
	for _, want := range []string{
		qaNoteHeading,
		"Verify passed: the panel renders and the totals add up",
		"Verify checks: tests passed, lint passed",
		"Browser QA: driven against http://app.test",
		"PR: " + qaNotePR,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("QA note is missing %q:\n%s", want, body)
		}
	}
	if len(tr.notes[0].Images) != 0 {
		t.Errorf("QA note carried %d images, want none in this slice", len(tr.notes[0].Images))
	}
}

// QA_NOTES=0 suppresses the delivery comment entirely.
func TestQANotesOffPostsNothingOnDelivery(t *testing.T) {
	id := "COD-91427"
	tr := newQATracker()
	p := newQADeliveryPipeline(t, tr, id)
	p.QANotes = false

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if len(tr.notes) != 0 {
		t.Errorf("posted %d QA notes with QA_NOTES off, want 0", len(tr.notes))
	}
}

// The report is best-effort: a tracker that rejects the comment must not cost the
// run its delivery.
func TestQANotePostFailureNeverFailsDelivery(t *testing.T) {
	id := "COD-91428"
	tr := newQATracker()
	tr.postErr = errors.New("comment endpoint unavailable")
	p := newQADeliveryPipeline(t, tr, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want the failed comment ignored", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q", got, state.PROpen)
	}
	if got := p.State.Get(id, "PR_URL"); got != qaNotePR {
		t.Errorf("PR_URL = %q, want the opened PR recorded", got)
	}
}

// A tracker without the comment capability is a silent no-op, not an error.
func TestDeliverySkipsQANoteWithoutTheCapability(t *testing.T) {
	id := "COD-91429"
	p := newQADeliveryPipeline(t, &fakeTracker{}, id)

	if err := p.CommitAndPR(context.Background(), id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}
	if got := p.State.Get(id, "PHASE"); got != state.PROpen {
		t.Errorf("PHASE = %q, want %q", got, state.PROpen)
	}
}

// A run that gives up after its repair and bugfix attempts leaves one comment
// carrying the verdict's failure lines and the HITL blocker it filed.
func TestTerminalFailurePostsQANoteWithFailureLinesAndBug(t *testing.T) {
	id := "COD-91430"
	writeHandoff(t, id)
	tr := newQATracker()
	runner := &verdictRunner{path: verifyPath(id), v: verdict{
		Summary:  "the dashboard stayed blank",
		Failures: []string{"panel renders nothing", "totals endpoint 500s"},
	}}
	p := newTestPipeline(t, runner, tr)
	p.QANotes = true

	err := p.Verify(context.Background(), id)

	var giveUp *GiveUpError
	if !errors.As(err, &giveUp) {
		t.Fatalf("Verify err = %v, want a *GiveUpError", err)
	}
	if len(tr.notes) != 1 {
		t.Fatalf("posted %d QA notes, want exactly 1", len(tr.notes))
	}
	body := tr.notes[0].Body
	for _, want := range []string{
		qaNoteHeading,
		"Verify failed: the dashboard stayed blank",
		"- panel renders nothing",
		"- totals endpoint 500s",
		"HITL blocker: BUG-1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("QA note is missing %q:\n%s", want, body)
		}
	}
}
