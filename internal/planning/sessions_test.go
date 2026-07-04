package planning

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSession writes a plan session at root/<id> with a chosen phase, and
// optionally an idea snapshot and a persisted PRD, so listing and resume can be
// exercised over durable artifacts without running a round.
func seedSession(t *testing.T, root, id, phase, idea string, prd *PRD) *Session {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Session{dir: dir, now: fixedClock()}
	if idea != "" {
		if err := s.writeIdea(idea); err != nil {
			t.Fatal(err)
		}
	}
	if prd != nil {
		if err := s.savePRD(*prd); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.setPhase(phase); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestListOrdersResumableFirstThenNewest pins the Plan screen's listing order:
// in-flight (resumable) sessions ahead of terminal ones, newest id first within
// each group. Ids are sortable creation timestamps, so descending id is newest.
func TestListOrdersResumableFirstThenNewest(t *testing.T) {
	root := t.TempDir()
	seedSession(t, root, "20260101-000001-000", PhaseQuestions, "one", nil)
	seedSession(t, root, "20260101-000002-000", PhaseAborted, "two", nil)
	seedSession(t, root, "20260101-000003-000", PhaseReview, "three", &PRD{Title: "Three", Markdown: "# Three"})
	seedSession(t, root, "20260101-000004-000", PhaseSliced, "four", nil)
	seedSession(t, root, "20260101-000005-000", PhaseDrafting, "five", nil)

	got := List(root)
	wantIDs := []string{
		"20260101-000005-000", // drafting  (resumable, newest)
		"20260101-000003-000", // prd_review
		"20260101-000001-000", // questions
		"20260101-000004-000", // sliced    (terminal, newest)
		"20260101-000002-000", // aborted
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("List returned %d sessions, want %d", len(got), len(wantIDs))
	}
	for i, si := range got {
		if si.ID != wantIDs[i] {
			t.Errorf("List[%d].ID = %q, want %q", i, si.ID, wantIDs[i])
		}
	}
}

// TestListProjectsLabels checks the row projection: the PRD title once one exists,
// the idea's first line as the fallback label, and Resumable tracking Terminal.
func TestListProjectsLabels(t *testing.T) {
	root := t.TempDir()
	seedSession(t, root, "20260101-000001-000", PhaseReview, "export widgets\nmore detail", &PRD{Title: "Widgets", Markdown: "# Widgets"})
	seedSession(t, root, "20260101-000002-000", PhaseQuestions, "  raw idea line  \n\nrest", nil)
	seedSession(t, root, "20260101-000003-000", PhaseAborted, "abandoned", nil)

	byID := map[string]SessionInfo{}
	for _, si := range List(root) {
		byID[si.ID] = si
	}

	if si := byID["20260101-000001-000"]; si.Title != "Widgets" || si.Idea != "export widgets" || !si.Resumable() {
		t.Errorf("review session projected as %+v", si)
	}
	if si := byID["20260101-000002-000"]; si.Title != "" || si.Idea != "raw idea line" || !si.Resumable() {
		t.Errorf("questions session projected as %+v", si)
	}
	if si := byID["20260101-000003-000"]; si.Resumable() {
		t.Error("aborted session should not be resumable")
	}
}

func TestListEmptyRoot(t *testing.T) {
	if got := List(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Errorf("List of a missing root = %v, want empty", got)
	}
}

// TestResumeStepForEveryPhase pins the checkpoint→step mapping every resume keys
// off, in the style of the state package's ranking table tests: each phase resolves
// to exactly one step, and any unknown checkpoint is treated as terminal.
func TestResumeStepForEveryPhase(t *testing.T) {
	tests := []struct {
		phase string
		want  ResumeStep
	}{
		{PhaseDrafting, StepRound},
		{PhaseQuestions, StepRound},
		{PhaseReview, StepReview},
		{PhasePRDReady, StepPublish},
		{PhasePublished, StepSlice},
		{PhaseSliced, StepDone},
		{PhaseAborted, StepDone},
		{"", StepDone},
		{"bogus", StepDone},
	}
	for _, tc := range tests {
		if got := ResumeStepFor(tc.phase); got != tc.want {
			t.Errorf("ResumeStepFor(%q) = %q, want %q", tc.phase, got, tc.want)
		}
	}
}

// TestResumeMidQuestionsReplaysNothing simulates a crash while answering the
// second round: the first round's answers are settled on the transcript, the
// second round's questions were never persisted. Resuming re-runs the current
// round from the idea and the settled transcript — it re-derives the pending
// questions, replays no completed round (the transcript stays one round), and the
// fresh prompt still carries the recorded first-round answer.
func TestResumeMidQuestionsReplaysNothing(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{finals: []string{
		`{"status":"questions","questions":[{"id":"q1","text":"who is the actor?","kind":"single","options":[{"label":"admins"},{"label":"editors"}]}]}`,
		`{"status":"questions","questions":[{"id":"q2","text":"what to name it?","kind":"text"}]}`,
		`{"status":"questions","questions":[{"id":"q2","text":"what to name it?","kind":"text"}]}`,
	}}
	o := NewOrchestrator(runner, root).WithClock(fixedClock()).WithMaxRounds(5)

	rr, err := o.RunRound(context.Background(), "let users export widgets")
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	sess := rr.Session

	if _, err := o.AnswerRound(context.Background(), sess, []Answer{
		{ID: "q1", Question: "who is the actor?", Values: []string{"editors"}},
	}); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if got := sess.Phase(); got != PhaseQuestions {
		t.Fatalf("phase before resume = %q, want questions", got)
	}
	if step := ResumeStepFor(sess.Phase()); step != StepRound {
		t.Fatalf("resume step = %q, want round", step)
	}

	resumed := OpenSession(sess.Dir())
	rr, err = o.ResumeRound(context.Background(), resumed)
	if err != nil {
		t.Fatalf("ResumeRound: %v", err)
	}
	if rr.Payload.Status != StatusQuestions {
		t.Fatalf("resumed status = %q, want questions", rr.Payload.Status)
	}

	transcript, err := resumed.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if len(transcript) != 1 {
		t.Fatalf("transcript has %d rounds after resume, want 1 — no completed round replayed", len(transcript))
	}
	if got := transcript[0].Answers[0].Values; len(got) != 1 || got[0] != "editors" {
		t.Errorf("recorded answer = %v, want [editors] preserved across resume", got)
	}
	if !strings.Contains(runner.prompts[2], "editors") {
		t.Error("resume prompt did not re-read the settled transcript answer")
	}
}

// TestAbortIsTerminalFromEveryPhase checks aborting from any pre-publish phase
// flips the checkpoint to the terminal aborted state, so resume treats it as done
// and it is no longer resumable in a listing.
func TestAbortIsTerminalFromEveryPhase(t *testing.T) {
	for _, phase := range []string{PhaseDrafting, PhaseQuestions, PhaseReview, PhasePRDReady} {
		root := t.TempDir()
		sess := seedSession(t, root, "20260101-000001-000", phase, "an idea", nil)

		if err := sess.Abort(); err != nil {
			t.Fatalf("Abort from %q: %v", phase, err)
		}
		if got := sess.Phase(); got != PhaseAborted {
			t.Errorf("Abort from %q left phase %q, want aborted", phase, got)
		}
		if !Terminal(sess.Phase()) {
			t.Errorf("aborted session from %q is not Terminal", phase)
		}
		if step := ResumeStepFor(sess.Phase()); step != StepDone {
			t.Errorf("resume step after abort from %q = %q, want done", phase, step)
		}
		if si := List(root)[0]; si.Resumable() {
			t.Errorf("aborted session from %q still lists as resumable", phase)
		}
	}
}

// TestAbortAfterPublishLeavesArtifacts checks a post-publish abort only flips the
// checkpoint: the already-persisted PRD and transcript — the analog of tracker
// issues already created — are left untouched.
func TestAbortAfterPublishLeavesArtifacts(t *testing.T) {
	root := t.TempDir()
	sess := seedSession(t, root, "20260101-000001-000", PhasePublished, "an idea", &PRD{Title: "Shipped", Markdown: "# Shipped\n\nbody"})
	if err := sess.AppendRound(QARound{Round: 1, Answers: []Answer{{ID: "q1", Values: []string{"a"}}}}); err != nil {
		t.Fatal(err)
	}

	if err := sess.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if got := sess.Phase(); got != PhaseAborted {
		t.Errorf("phase = %q, want aborted", got)
	}
	prd, ok := sess.PRD()
	if !ok || prd.Title != "Shipped" || !strings.Contains(prd.Markdown, "body") {
		t.Errorf("aborting a published session disturbed the PRD: %+v (ok=%v)", prd, ok)
	}
	transcript, _ := sess.Transcript()
	if len(transcript) != 1 {
		t.Errorf("aborting a published session disturbed the transcript: %v", transcript)
	}
}
