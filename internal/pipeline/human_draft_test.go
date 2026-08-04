package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// A branch a human parked behind a draft PR is left exactly as it is: the slice's
// commits are already pushed, but nothing adopts the PR, undrafts it or merges it.
// The run stops blamelessly with the draft named in the reason so the operator can
// see what it is waiting on.
func TestCommitAndPRPausesOnHumanDraftPR(t *testing.T) {
	const (
		id       = "COD-96410"
		draftURL = "https://github.test/pr/31"
	)
	git := &localGit{hasRemote: true, branch: "feature/COD-96410-thing"}
	gh := &epicGitHub{openURL: draftURL, openDraft: true}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	if err := p.State.Set(id, "BRANCH", "feature/COD-96410-thing"); err != nil {
		t.Fatal(err)
	}

	err := p.CommitAndPR(context.Background(), id)

	paused := AsPaused(err)
	if paused == nil {
		t.Fatalf("CommitAndPR = %v, want a blameless pause", err)
	}
	if !strings.Contains(paused.Reason, draftURL) {
		t.Errorf("pause reason = %q, want it to name %s", paused.Reason, draftURL)
	}
	if gh.createCalls != 0 {
		t.Errorf("opened %d PR(s) over a human's draft, want 0", gh.createCalls)
	}
	if gh.readyCalls != 0 {
		t.Errorf("undrafted a human's PR %d time(s), want 0", gh.readyCalls)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("merged a human's draft %d time(s), want 0", gh.mergeCalls)
	}
	if got := p.State.Get(id, "FAILURE_CLASS"); got != state.FailPaused {
		t.Errorf("FAILURE_CLASS = %q, want %q", got, state.FailPaused)
	}
}

// An epic draft with no marker naming it as trau's own is a human's, so finalize
// hands the release over instead of shipping it — the same outcome as a draft trau
// knows it never opened.
func TestFinalizeEpicHandsOffUnmarkedDraft(t *testing.T) {
	const draftURL = "https://github.test/pr/42"
	gh := &epicGitHub{
		openURL:   draftURL,
		openDraft: true,
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, doneEpicTracker())

	err := p.FinalizeEpic(context.Background())

	var handOff *EpicHandOffError
	if !errors.As(err, &handOff) {
		t.Fatalf("FinalizeEpic = %v, want an *EpicHandOffError", err)
	}
	if handOff.PRURL != draftURL {
		t.Errorf("hand-off PR = %q, want %q", handOff.PRURL, draftURL)
	}
	if gh.readyCalls != 0 {
		t.Errorf("gh pr ready calls = %d, want 0 — a human's draft is not trau's to undraft", gh.readyCalls)
	}
	if gh.mergeCalls != 0 {
		t.Errorf("gh pr merge calls = %d, want 0", gh.mergeCalls)
	}
	if got := p.State.Get("COD-1", "RELEASE"); got != state.ReleaseAwaitingHuman {
		t.Errorf("epic RELEASE = %q, want %q", got, state.ReleaseAwaitingHuman)
	}
}

// The draft trau's own exit hygiene opened still ships: the marker it recorded is
// read back off the checkpoint by the later run that finalizes the epic — a
// different process, which is exactly when this decision is made.
func TestFinalizeEpicShipsItsOwnDraft(t *testing.T) {
	const (
		epic     = "epic/COD-1-checkout-rebuild"
		draftURL = "https://github.test/pr/42"
	)
	root := t.TempDir()
	opener := shippableEpicPipeline(t, &epicGitHub{createURL: draftURL}, doneEpicTracker())
	opener.State = state.NewStore(root)
	if _, _, err := opener.ensureEpicPR(context.Background(), epic, true); err != nil {
		t.Fatalf("ensureEpicPR = %v, want the exit-hygiene draft opened", err)
	}

	gh := &epicGitHub{
		openURL:   draftURL,
		openDraft: true,
		checks:    []Check{{Name: "ci/test", Bucket: "pass"}},
	}
	p := shippableEpicPipeline(t, gh, doneEpicTracker())
	p.State = state.NewStore(root)

	if err := p.FinalizeEpic(context.Background()); err != nil {
		t.Fatalf("FinalizeEpic = %v, want the epic shipped", err)
	}
	if gh.readyCalls != 1 {
		t.Errorf("gh pr ready calls = %d, want 1 — trau's own draft is undrafted before the merge", gh.readyCalls)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("gh pr merge calls = %d, want 1", gh.mergeCalls)
	}
}
