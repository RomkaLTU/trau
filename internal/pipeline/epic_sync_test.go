package pipeline

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// epicSyncGit models the two copies of one epic branch — the local one the sync
// works on and the one the remote reports — and records every operation, so a test
// can assert both the steps the sync took and the state it left behind.
type epicSyncGit struct {
	fakeGit
	ops []string

	branch string // the epic branch under sync
	head   string // the local epic tip
	remote string // the tip ls-remote reports for the epic

	pullErr    error    // what pull --ff-only answers
	behind     bool     // the local tip is an ancestor of the remote tip
	ahead      bool     // the remote tip is an ancestor of the local tip
	localOnly  []string // non-merge commits only the local copy carries
	mergedBase []string // base commits a swallowed sync merge dragged into the range
	revListErr error
	resetErr   error
	conflicted bool    // the first MergeRemote stops on conflicts
	pushErrs   []error // consumed in order by the pushes of the epic branch

	merges  int
	pushes  int
	creates int
}

func (g *epicSyncGit) op(format string, a ...any) { g.ops = append(g.ops, fmt.Sprintf(format, a...)) }

func (g *epicSyncGit) Checkout(_ context.Context, ref string, _ bool) error {
	g.op("checkout %s", ref)
	return nil
}

func (g *epicSyncGit) Pull(_ context.Context, remote, branch string) error {
	g.op("pull %s %s", remote, branch)
	return g.pullErr
}

func (g *epicSyncGit) HeadSHA(context.Context) (string, error) { return g.head, nil }

func (g *epicSyncGit) RemoteSHA(context.Context, string, string) (string, error) {
	return g.remote, nil
}

func (g *epicSyncGit) ResolvesToCommit(context.Context, string) (bool, error) { return true, nil }

// IsAncestor answers the two ancestry questions the divergence check asks, told
// apart by which side is named as the ancestor.
func (g *epicSyncGit) IsAncestor(_ context.Context, ancestor, rev string) (bool, error) {
	switch {
	case ancestor == g.branch && rev == g.remote:
		return g.behind, nil
	case ancestor == g.remote && rev == g.branch:
		return g.ahead, nil
	}
	return false, nil
}

// RevListNoMerges models the walk the real one does: base..head crosses a sync
// merge's second parent, so the base commits that merge brought over are in the range
// until the caller excludes the base tip.
func (g *epicSyncGit) RevListNoMerges(_ context.Context, base, head string, exclude ...string) ([]string, error) {
	g.op("revList %s..%s ^%s", base, head, strings.Join(exclude, " ^"))
	commits := slices.Clone(g.localOnly)
	if !slices.Contains(exclude, syncEpicBaseTip) {
		commits = append(commits, g.mergedBase...)
	}
	return commits, g.revListErr
}

func (g *epicSyncGit) ResetHard(_ context.Context, rev string) error {
	g.op("reset %s", rev)
	if g.resetErr != nil {
		return g.resetErr
	}
	g.head = rev
	return nil
}

func (g *epicSyncGit) MergeRemote(_ context.Context, remote, base string) (bool, error) {
	g.op("mergeRemote %s %s", remote, base)
	if g.conflicted {
		g.conflicted = false
		return true, nil
	}
	g.merges++
	g.head = fmt.Sprintf("syncmerge%d", g.merges)
	return false, nil
}

func (g *epicSyncGit) MergeAbort(context.Context) error {
	g.op("mergeAbort")
	return nil
}

func (g *epicSyncGit) Push(_ context.Context, remote, ref string, _ bool) error {
	g.op("push %s %s", remote, ref)
	var err error
	if ref == g.branch && g.pushes < len(g.pushErrs) {
		err = g.pushErrs[g.pushes]
	}
	g.pushes++
	if err != nil {
		return err
	}
	if ref == g.branch {
		g.remote = g.head
	}
	return nil
}

func (g *epicSyncGit) CreateBranch(_ context.Context, branch, base string) error {
	g.creates++
	g.op("createBranch %s %s", branch, base)
	return nil
}

func (g *epicSyncGit) FindEpicBranch(context.Context, string) (string, error) {
	return g.branch, nil
}

const (
	syncEpicBranch  = "epic/COD-1520-cart-step"
	syncEpicRemote  = "8c2f01d38c2f01d38c2f01d38c2f01d38c2f01d3"
	syncEpicLocal   = "2be7a4402be7a4402be7a4402be7a4402be7a440"
	syncEpicBaseTip = "origin/main"
	syncSliceID     = "COD-1521"
)

// newEpicSyncPipeline is a classic-epic run holding the epic branch, so the branch
// cut for its next slice goes through the epic sync.
func newEpicSyncPipeline(t *testing.T, git Git) *Pipeline {
	t.Helper()
	p := newTestPipeline(t, fakeRunner{}, &epicTracker{title: "Cart step"})
	p.Git = git
	p.Delivery = &epicGitHub{}
	p.Remote = "origin"
	p.EpicID = "COD-1520"
	p.exit.epicBranch = syncEpicBranch
	return p
}

// A push that never lands must not leave the sync merge behind: the local epic goes
// back to the tip it had before the merge. Otherwise the next sibling squash-merges
// into the remote epic, both sides carry commits the other lacks, and no later run
// can reconcile them.
func TestSyncEpicBestRollsBackAnUnpushedSyncMerge(t *testing.T) {
	rejected := errors.New("! [rejected] epic/COD-1520-cart-step (non-fast-forward)")
	git := &epicSyncGit{
		branch:   syncEpicBranch,
		head:     syncEpicLocal,
		remote:   syncEpicLocal,
		pushErrs: []error{rejected, rejected},
	}
	p := newEpicSyncPipeline(t, git)

	if err := p.syncEpicBest(context.Background(), syncEpicBranch); err != nil {
		t.Fatalf("syncEpicBest = %v, want nil (a failed push stays best-effort)", err)
	}
	if git.head != syncEpicLocal {
		t.Errorf("local epic tip = %q, want it rolled back to %q", git.head, syncEpicLocal)
	}
	if git.head != git.remote {
		t.Errorf("local tip %q is ahead of the remote tip %q after the sync", git.head, git.remote)
	}
	if got, want := strings.Count(strings.Join(git.ops, ";"), "reset "+syncEpicLocal), 2; got != want {
		t.Errorf("rolled back %d time(s), want %d — once per failed push attempt", got, want)
	}
	if git.pushes != 2 {
		t.Errorf("pushes = %d, want 2 — one retry for the pull↔push race", git.pushes)
	}
}

// The race the retry exists for: a sibling squash-merges into the remote epic
// between this run's pull and its push. The first push is rejected, the merge is
// rolled back, and the second pull→merge→push lands.
func TestSyncEpicBestRetriesAfterTheRemoteMovedUnderThePush(t *testing.T) {
	git := &epicSyncGit{
		branch:   syncEpicBranch,
		head:     syncEpicLocal,
		remote:   syncEpicLocal,
		pushErrs: []error{errors.New("! [rejected] (fetch first)")},
	}
	p := newEpicSyncPipeline(t, git)

	if err := p.syncEpicBest(context.Background(), syncEpicBranch); err != nil {
		t.Fatalf("syncEpicBest = %v, want nil", err)
	}
	want := []string{
		"checkout " + syncEpicBranch,
		"pull origin " + syncEpicBranch,
		"mergeRemote origin main",
		"push origin " + syncEpicBranch,
		"reset " + syncEpicLocal,
		"pull origin " + syncEpicBranch,
		"mergeRemote origin main",
		"push origin " + syncEpicBranch,
	}
	if got := strings.Join(git.ops, "; "); got != strings.Join(want, "; ") {
		t.Errorf("git ops = %q, want %q", got, strings.Join(want, "; "))
	}
	if git.head != git.remote {
		t.Errorf("local tip %q, remote tip %q — want the retried sync pushed", git.head, git.remote)
	}
}

// A merge that stops on conflicts commits nothing, so there is nothing to roll back
// and nothing to retry: the resolution belongs to the finalize sync, which runs a
// resolving agent.
func TestSyncEpicBestLeavesNothingBehindOnAConflictedMerge(t *testing.T) {
	git := &epicSyncGit{
		branch:     syncEpicBranch,
		head:       syncEpicLocal,
		remote:     syncEpicLocal,
		conflicted: true,
	}
	p := newEpicSyncPipeline(t, git)

	if err := p.syncEpicBest(context.Background(), syncEpicBranch); err != nil {
		t.Fatalf("syncEpicBest = %v, want nil", err)
	}
	if git.head != syncEpicLocal || git.head != git.remote {
		t.Errorf("local tip %q, remote tip %q — want both left at %q", git.head, git.remote, syncEpicLocal)
	}
	for _, op := range git.ops {
		if strings.HasPrefix(op, "reset ") {
			t.Errorf("git ops = %q, want no rollback of a merge that never committed", git.ops)
			break
		}
	}
}

// Divergence whose local-only commits are all sync merges carries no unique work:
// the remote is authoritative — siblings land there — so the local epic is reset
// onto it, resynced and pushed, and the slice is cut as usual.
//
// This is the shape a swallowed sync push actually leaves: the merge dragged the
// base's own commits over with it, and they must not read as work only this machine
// has — the epic is reset onto the remote tip and the base merged again right after.
func TestResolveBuildBranchHealsADivergedEpicWithSyncMergesOnly(t *testing.T) {
	git := &epicSyncGit{
		branch:     syncEpicBranch,
		head:       syncEpicLocal,
		remote:     syncEpicRemote,
		pullErr:    errors.New("fatal: Not possible to fast-forward, aborting"),
		mergedBase: []string{"c59978c feat(catalog): main moves on"},
	}
	p := newEpicSyncPipeline(t, git)

	branch, err := p.resolveBuildBranch(context.Background(), syncSliceID)
	if err != nil {
		t.Fatalf("resolveBuildBranch = %v, want the slice cut from the healed epic", err)
	}
	if !strings.HasPrefix(branch, "feature/"+syncSliceID) {
		t.Errorf("branch = %q, want the slice's feature branch", branch)
	}
	if git.creates != 1 {
		t.Errorf("CreateBranch calls = %d, want 1", git.creates)
	}
	if got := strings.Join(git.ops, "; "); !strings.Contains(got, "reset "+syncEpicRemote) {
		t.Errorf("git ops = %q, want the local epic reset onto the remote tip", got)
	}
	if git.head != "syncmerge1" || git.remote != "syncmerge1" {
		t.Errorf("local tip %q, remote tip %q — want both at the resynced merge", git.head, git.remote)
	}
}

// divergedEpicRepo builds, for real, the shape a swallowed sync push leaves behind:
// the local epic carries a merge of the base that never landed, the remote epic
// carries a sibling's squash, and each side holds commits the other lacks — so
// pull --ff-only refuses. It returns the work tree and the epic branch.
func divergedEpicRepo(t *testing.T) (work, epic string) {
	t.Helper()
	epic = syncEpicBranch

	origin := t.TempDir()
	gitRun(t, origin, "init", "--bare", "-b", "main")

	work = t.TempDir()
	gitRun(t, work, "clone", origin, work)
	gitRun(t, work, "checkout", "-b", "main")
	writeRepoFile(t, work, "a.txt", "base\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "chore: init")
	gitRun(t, work, "push", "origin", "main")

	gitRun(t, work, "checkout", "-b", epic)
	writeRepoFile(t, work, "epic.txt", "epic\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "feat(cart): epic seed")
	gitRun(t, work, "push", "origin", epic)

	gitRun(t, work, "checkout", "main")
	writeRepoFile(t, work, "b.txt", "moved\n")
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "commit", "-m", "feat(catalog): main moves on")
	gitRun(t, work, "push", "origin", "main")

	// the sync merge this machine made and never managed to push
	gitRun(t, work, "checkout", epic)
	gitRun(t, work, "fetch", "origin", "main")
	gitRun(t, work, "merge", "--no-edit", "FETCH_HEAD")

	// meanwhile a sibling slice squash-merges into the remote epic
	sibling := t.TempDir()
	gitRun(t, sibling, "clone", origin, sibling)
	gitRun(t, sibling, "checkout", epic)
	writeRepoFile(t, sibling, "sibling.txt", "sibling\n")
	gitRun(t, sibling, "add", "-A")
	gitRun(t, sibling, "commit", "-m", "feat(cart): sibling slice")
	gitRun(t, sibling, "push", "origin", epic)

	return work, epic
}

// The heal against real git, which is where the classification has to hold: a merge
// drags the merged branch's commits into remoteTip..epic, so a walk that counts them
// as local work reads trau's own disposable merge as unpushed work and refuses to
// heal the very case this exists for.
func TestSyncEpicBestHealsASwallowedSyncMergeAgainstRealGit(t *testing.T) {
	work, epic := divergedEpicRepo(t)
	p := newEpicSyncPipeline(t, ExecGit{Repo: work})

	if err := p.syncEpicBest(context.Background(), epic); err != nil {
		t.Fatalf("syncEpicBest = %v, want the diverged epic healed", err)
	}

	if got := gitOut(t, work, "rev-list", "--left-right", "--count", epic+"...origin/"+epic); got != "0\t0" {
		t.Errorf("left-right count = %q, want %q — the two copies must be level again", got, "0\t0")
	}
	log := gitOut(t, work, "log", "--oneline", epic)
	for _, want := range []string{"feat(cart): sibling slice", "feat(catalog): main moves on"} {
		if !strings.Contains(log, want) {
			t.Errorf("epic log =\n%s\nwant it to carry %q", log, want)
		}
	}
}

// Divergence with real unpushed work is the case trau may not resolve on its own:
// resetting drops the work and force-pushing rewrites a shared branch. The run
// stops before the slice branch exists, naming the commits and the repair.
func TestResolveBuildBranchFailsFastOnADivergedEpicWithLocalWork(t *testing.T) {
	git := &epicSyncGit{
		branch:    syncEpicBranch,
		head:      syncEpicLocal,
		remote:    syncEpicRemote,
		pullErr:   errors.New("fatal: Not possible to fast-forward, aborting"),
		localOnly: []string{"7f31a09 fix(cart): unavailable products section"},
	}
	p := newEpicSyncPipeline(t, git)

	_, err := p.resolveBuildBranch(context.Background(), syncSliceID)
	if err == nil {
		t.Fatal("resolveBuildBranch = nil, want the diverged-epic fault")
	}
	if isGiveUp(err) {
		t.Errorf("resolveBuildBranch = %v, want a resumable fault, not a give-up", err)
	}
	for _, want := range []string{
		syncEpicBranch,
		syncEpicRemote,
		"7f31a09 fix(cart): unavailable products section",
		"git rev-list --left-right --oneline",
		"git reset --hard origin/" + syncEpicBranch,
		"rebase",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v\nwant it to carry %q", err, want)
		}
	}
	if git.creates != 0 {
		t.Errorf("CreateBranch calls = %d, want 0 — no slice may be cut from a diverged epic", git.creates)
	}
}

// Established divergence that cannot be classified is unsafe, not permission to
// proceed: a slice cut here would be pinned to a commit the remote never carries,
// and the whole build would burn before the commit/PR gate refused it.
func TestSyncEpicBestFailsWhenDivergenceCannotBeClassified(t *testing.T) {
	git := &epicSyncGit{
		branch:     syncEpicBranch,
		head:       syncEpicLocal,
		remote:     syncEpicRemote,
		pullErr:    errors.New("fatal: Not possible to fast-forward, aborting"),
		revListErr: errors.New("fatal: bad revision"),
	}
	p := newEpicSyncPipeline(t, git)

	err := p.syncEpicBest(context.Background(), syncEpicBranch)
	if err == nil || !strings.Contains(err.Error(), "has diverged from") {
		t.Fatalf("syncEpicBest = %v, want the diverged-epic failure", err)
	}
}

// A local epic the remote is ahead of behaves as it always did: the pull failed for
// its own reasons, nothing is reset, and the sync carries on best-effort.
func TestSyncEpicBestLeavesABehindEpicAlone(t *testing.T) {
	git := &epicSyncGit{
		branch:  syncEpicBranch,
		head:    syncEpicLocal,
		remote:  syncEpicRemote,
		behind:  true,
		pullErr: errors.New("error: Your local changes would be overwritten"),
	}
	p := newEpicSyncPipeline(t, git)

	if err := p.syncEpicBest(context.Background(), syncEpicBranch); err != nil {
		t.Fatalf("syncEpicBest = %v, want nil", err)
	}
	for _, op := range git.ops {
		if strings.HasPrefix(op, "reset ") || strings.HasPrefix(op, "revList ") {
			t.Fatalf("git ops = %q, want a branch that is only behind left untouched", git.ops)
		}
	}
	if git.head != git.remote {
		t.Errorf("local tip %q, remote tip %q — want the sync pushed as before", git.head, git.remote)
	}
}

// An epic ahead of its remote is what the sync push repairs, so it must not be
// classified as divergence and must not be reset.
func TestSyncEpicBestPushesAnAheadEpic(t *testing.T) {
	git := &epicSyncGit{
		branch:  syncEpicBranch,
		head:    syncEpicLocal,
		remote:  syncEpicRemote,
		ahead:   true,
		pullErr: errors.New("fatal: Not possible to fast-forward, aborting"),
	}
	p := newEpicSyncPipeline(t, git)

	if err := p.syncEpicBest(context.Background(), syncEpicBranch); err != nil {
		t.Fatalf("syncEpicBest = %v, want nil", err)
	}
	for _, op := range git.ops {
		if strings.HasPrefix(op, "reset ") {
			t.Fatalf("git ops = %q, want an ahead-only epic left for the push to repair", git.ops)
		}
	}
	if git.head != "syncmerge1" || git.remote != "syncmerge1" {
		t.Errorf("local tip %q, remote tip %q — want the sync merge pushed", git.head, git.remote)
	}
}

// The stale-base failure is what an operator reads when the run stops at the PR
// gate, so it carries the same runbook the fail-fast path does.
func TestStaleBaseNoteCarriesTheRecoverySteps(t *testing.T) {
	note := staleBaseNote("origin", "epic/COD-1520-cart-step", syncEpicRemote)
	for _, want := range []string{
		"does not carry " + syncEpicRemote,
		"git fetch origin epic/COD-1520-cart-step",
		"git rev-list --left-right --oneline epic/COD-1520-cart-step...origin/epic/COD-1520-cart-step",
		"git reset --hard origin/epic/COD-1520-cart-step",
		"rebase them onto origin/epic/COD-1520-cart-step",
		"Rebase that slice branch onto the repaired base",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("staleBaseNote = %q\nwant it to carry %q", note, want)
		}
	}
}

// The stacked shape gates its layers through the same assertion, so a wedged stack
// hands the operator the same steps as a classic epic.
func TestStackedBaseFailureCarriesTheRecoverySteps(t *testing.T) {
	git := &prBaseGit{localGit: localGit{hasRemote: true, branch: prBaseRunBranch}}
	p := newSlicePRPipeline(t, git, &countingGitHub{})

	err := p.assertStackedBaseCurrent(context.Background(), prBaseTicketID, prBaseBranch)
	if err == nil {
		t.Fatal("assertStackedBaseCurrent = nil, want the stale-base failure")
	}
	for _, want := range []string{"Recovery steps:", "git rev-list --left-right --oneline", "git reset --hard origin/main"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("assertStackedBaseCurrent = %v\nwant it to carry %q", err, want)
		}
	}
}
