package pipeline

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// fakeRunner returns the same canned result/error for every phase call, and,
// when given a log, records each prompt it was handed.
type fakeRunner struct {
	err   error
	calls *promptLog
}

func (r fakeRunner) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	if r.calls != nil {
		r.calls.record(label, prompt)
	}
	return agent.Result{}, r.err
}

// promptLog collects the prompts a fakeRunner was called with. Safe for the
// concurrent verify panel.
type promptLog struct {
	mu    sync.Mutex
	calls []promptCall
}

type promptCall struct{ label, prompt string }

func (l *promptLog) record(label, prompt string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, promptCall{label: label, prompt: prompt})
}

func (l *promptLog) all() []promptCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]promptCall(nil), l.calls...)
}

// fakeTracker records whether the loop reached the quarantine/file-bug path.
type fakeTracker struct {
	fileBugCalls    int
	quarantineCalls int
}

func (t *fakeTracker) Pick(context.Context, tracker.Scope) (string, error) { return "", nil }
func (t *fakeTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) {
	return nil, nil
}
func (t *fakeTracker) Title(context.Context, string) (string, error)           { return "", nil }
func (t *fakeTracker) SetStatus(context.Context, string, string, string) error { return nil }
func (t *fakeTracker) Reset(context.Context, string) error                     { return nil }
func (t *fakeTracker) Quarantine(context.Context, string, string) error {
	t.quarantineCalls++
	return nil
}
func (t *fakeTracker) FileBug(context.Context, string, string) (string, error) {
	t.fileBugCalls++
	return "BUG-1", nil
}
func (t *fakeTracker) EnsureLabels(context.Context) error { return nil }

// fakeGit is a no-op git whose CurrentBranch equals the base, so finalizeFailed
// takes its shortest path (checkout+clean, no commit/push).
type fakeGit struct{}

func (fakeGit) CurrentBranch(context.Context) (string, error)      { return "main", nil }
func (fakeGit) AddAll(context.Context) error                       { return nil }
func (fakeGit) Commit(context.Context, string, bool) error         { return nil }
func (fakeGit) Push(context.Context, string, string, bool) error   { return nil }
func (fakeGit) PushDryRun(context.Context, string, string) error   { return nil }
func (fakeGit) Checkout(context.Context, string, bool) error       { return nil }
func (fakeGit) CreateBranch(context.Context, string, string) error { return nil }
func (fakeGit) Clean(context.Context) error                        { return nil }
func (fakeGit) BranchExists(context.Context, string) (bool, error) { return false, nil }
func (fakeGit) FindFeatureBranch(context.Context, string) (string, error) {
	return "", nil
}
func (fakeGit) FindEpicBranch(context.Context, string) (string, error) {
	return "", nil
}
func (fakeGit) FindRemoteEpicBranch(context.Context, string, string) (string, error) {
	return "", nil
}
func (fakeGit) DeleteBranch(context.Context, string) error { return nil }
func (fakeGit) DeletePushedBranch(context.Context, string, string) error {
	return nil
}
func (fakeGit) StatusPorcelain(context.Context) (string, error)           { return "", nil }
func (fakeGit) WorktreeDirty(context.Context) (bool, error)               { return true, nil }
func (fakeGit) Stash(context.Context, string) error                       { return nil }
func (fakeGit) StashPop(context.Context) error                            { return nil }
func (fakeGit) Commits(context.Context, string, string) ([]string, error) { return nil, nil }
func (fakeGit) CommitSubject(context.Context, string) (string, error)     { return "", nil }
func (fakeGit) Pull(context.Context, string, string) error                { return nil }
func (fakeGit) MergeRemote(context.Context, string, string) (bool, error) { return false, nil }
func (fakeGit) MergeAbort(context.Context) error                          { return nil }
func (fakeGit) Unmerged(context.Context) (string, error)                  { return "", nil }
func (fakeGit) ContinueMerge(context.Context) error                       { return nil }
func (fakeGit) RemoteBranchExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (fakeGit) CheckoutRemoteBranch(context.Context, string, string) error { return nil }

// writeHandoff drops a non-empty handoff brief so Verify skips regeneration and
// goes straight to the verify attempt (where the bug lives). Cleans up the /tmp
// artifacts afterward.
func writeHandoff(t *testing.T, id string) {
	t.Helper()
	if err := os.WriteFile(handoffPath(id), []byte("QA brief: check the thing.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(handoffPath(id))
		_ = os.Remove(verifyPath(id))
		_ = os.Remove(rubricPath(id))
	})
}

func newTestPipeline(t *testing.T, runner agent.Runner, tr tracker.Tracker) *Pipeline {
	t.Helper()
	dir := t.TempDir()
	return &Pipeline{
		Runner:       runner,
		Tracker:      tr,
		Git:          fakeGit{},
		State:        state.NewStore(dir),
		PhaseLogs:    newMemPhaseLogs(),
		LessonLedger: &fakeLedger{},
		RunsDir:      dir,
		Base:         "main",
		Prefix:       "COD",
		Lessons:      true,
		MaxRepairs:   0,
		MaxBugfixes:  0,
	}
}

// TestVerifyRateLimitPausesInsteadOfQuarantine is the COD-581 regression guard: a
// provider rate/usage limit during the single-verifier verify must PAUSE the
// ticket (resumable on its branch), never cascade into repair → quarantine →
// FileBug, and never pollute the lessons ledger.
func TestVerifyRateLimitPausesInsteadOfQuarantine(t *testing.T) {
	id := "COD-90581"
	writeHandoff(t, id)
	tr := &fakeTracker{}
	p := newTestPipeline(t, fakeRunner{err: errors.New("kimi run (verify): 429 usage limit reached")}, tr)

	err := p.Verify(context.Background(), id)

	if !IsPaused(err) {
		t.Fatalf("Verify err = %v, want a *PausedError", err)
	}
	if tr.fileBugCalls != 0 {
		t.Errorf("FileBug called %d times, want 0 (no bogus HITL bug on a rate limit)", tr.fileBugCalls)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0 (ticket must stay resumable)", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got == state.Quarantined {
		t.Errorf("PHASE = quarantined, want it left in-flight")
	}
	if lessons := p.LessonLedger.(*fakeLedger).records(); len(lessons) != 0 {
		t.Errorf("recorded %d lessons on a rate-limit pause, want 0", len(lessons))
	}
}

// TestVerifyPlainFailureQuarantines guards the other side: a non-rate-limit verify
// failure must still drive the normal repair-exhausted → quarantine + FileBug path
// (and record a real lesson), so the COD-581 fix doesn't suppress genuine failures.
func TestVerifyPlainFailureQuarantines(t *testing.T) {
	id := "COD-90582"
	writeHandoff(t, id)
	tr := &fakeTracker{}
	p := newTestPipeline(t, fakeRunner{err: errors.New("agent crashed: boom")}, tr)

	err := p.Verify(context.Background(), id)

	if IsPaused(err) {
		t.Fatalf("a plain (non-rate-limit) error must NOT pause: %v", err)
	}
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("Verify err = %v, want a *GiveUpError (quarantine)", err)
	}
	if tr.fileBugCalls != 1 {
		t.Errorf("FileBug calls = %d, want 1", tr.fileBugCalls)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine calls = %d, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
	if lessons := p.LessonLedger.(*fakeLedger).records(); len(lessons) != 1 {
		t.Errorf("recorded %d lessons on a real quarantine, want 1", len(lessons))
	}
}
