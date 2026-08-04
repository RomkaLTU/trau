package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

const noCommitsErr = `gh pr create: exit status 1: pull request create failed: GraphQL: No commits between main and feature/x (createPullRequest)`

// mergedAfterCreateGitHub reports the branch's merged PR only once a create has
// been attempted, so a test reaches the create-failure rescue rather than the
// probe that runs before the PR is opened.
type mergedAfterCreateGitHub struct {
	epicGitHub
}

func (g *mergedAfterCreateGitHub) MergedPRURL(context.Context, string) (string, error) {
	if g.createCalls == 0 {
		return "", nil
	}
	return g.mergedURL, nil
}

// Ship must not reopen work that already landed: a squash-merged head is no longer
// an ancestor of its base, so a fresh create carries the whole diff a second time
// and merging it applies the slice twice. A merged PR for the head settles the
// ticket instead; a head without one is ordinary new work and still gets its PR.
func TestCommitAndPRAdoptsMergedWorkInsteadOfReopening(t *testing.T) {
	const (
		mergedURL  = "https://github.test/pr/51"
		createdURL = "https://github.test/pr/52"
	)
	cases := []struct {
		name        string
		mergedURL   string
		wantErr     error
		wantCreates int
		wantPhase   string
		wantPRURL   string
	}{
		{
			name:      "head already merged",
			mergedURL: mergedURL,
			wantErr:   ErrAlreadyDone,
			wantPhase: state.Merged,
			wantPRURL: mergedURL,
		},
		{
			name:        "genuinely new work",
			wantCreates: 1,
			wantPhase:   state.PROpen,
			wantPRURL:   createdURL,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const (
				id     = "COD-96420"
				branch = "feature/COD-96420-checkout-totals"
			)
			git := &localGit{hasRemote: true, branch: branch}
			gh := &countingGitHub{epicGitHub: epicGitHub{createURL: createdURL, mergedURL: tc.mergedURL}}
			p := localTestPipeline(t, git, gh, &epicTracker{})
			mustSet(t, p, id, "BRANCH", branch)

			err := p.CommitAndPR(context.Background(), id)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("CommitAndPR = %v, want %v", err, tc.wantErr)
			}
			if gh.createCalls != tc.wantCreates {
				t.Errorf("opened %d PR(s), want %d", gh.createCalls, tc.wantCreates)
			}
			if got := p.State.Get(id, "PHASE"); got != tc.wantPhase {
				t.Errorf("PHASE = %q, want %q", got, tc.wantPhase)
			}
			if got := p.State.Get(id, "PR_URL"); got != tc.wantPRURL {
				t.Errorf("PR_URL = %q, want %q", got, tc.wantPRURL)
			}
		})
	}
}

// The create-failure side of the same rescue: GitHub refusing the create because
// base already carries the head is the epic path's signal too, and it must settle
// the slice as delivered rather than surfacing as a fault.
func TestCommitAndPRRescuesRefusedCreateWithMergedPR(t *testing.T) {
	const (
		id        = "COD-96421"
		branch    = "feature/COD-96421-invoice-totals"
		mergedURL = "https://github.test/pr/53"
	)
	git := &localGit{hasRemote: true, branch: branch}
	gh := &mergedAfterCreateGitHub{epicGitHub{createErr: errors.New(noCommitsErr), mergedURL: mergedURL}}
	p := localTestPipeline(t, git, gh, &epicTracker{})
	p.Sleep = func(time.Duration) {}
	mustSet(t, p, id, "BRANCH", branch)

	err := p.CommitAndPR(context.Background(), id)

	if !errors.Is(err, ErrAlreadyDone) {
		t.Fatalf("CommitAndPR = %v, want ErrAlreadyDone", err)
	}
	if gh.createCalls != 1 {
		t.Errorf("attempted %d create(s), want 1 — the refusal is deterministic", gh.createCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want %q", got, state.Merged)
	}
	if got := p.State.Get(id, "PR_URL"); got != mergedURL {
		t.Errorf("PR_URL = %q, want the merged PR %q", got, mergedURL)
	}
}

// The folder analogue: a Child repo whose branch already shipped adopts its merged
// PR, while the sibling carrying genuinely new work opens one as usual.
func TestFolderShipAdoptsAChildsMergedPR(t *testing.T) {
	const (
		id     = "COD-96422"
		branch = "feature/COD-96422-cross-repo-slice"
	)
	root := t.TempDir()
	gits := map[string]*childGit{"api-billing": {}, "api-companies": {}}
	ghs := map[string]*epicGitHub{
		"api-billing":   {mergedURL: "https://github.com/acme/api-billing/pull/5"},
		"api-companies": {createURL: "https://github.com/acme/api-companies/pull/3"},
	}
	makeChildRepos(t, root, "api-billing", "api-companies")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	mustSet(t, p, id, "BRANCH", branch)

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	for _, g := range gits {
		g.status = " M service.go"
	}

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR = %v, want nil", err)
	}

	if ghs["api-billing"].createCalls != 0 {
		t.Errorf("opened %d PR(s) over api-billing's merged work, want 0", ghs["api-billing"].createCalls)
	}
	if ghs["api-companies"].createCalls != 1 {
		t.Errorf("opened %d PR(s) for api-companies' new work, want 1", ghs["api-companies"].createCalls)
	}
	urls := p.State.Get(id, "PR_URLS")
	for _, want := range []string{
		"api-billing=" + ghs["api-billing"].mergedURL,
		"api-companies=" + ghs["api-companies"].createURL,
	} {
		if !strings.Contains(urls, want) {
			t.Errorf("PR_URLS = %q, want it to carry %q", urls, want)
		}
	}
}

// The downstream half of that adoption: the gate has nothing to wait for and
// nothing to merge in the child whose PR already landed — gh refuses a second
// merge with a failure no retry gets past — while the sibling's real PR still
// merges and the ticket lands.
func TestFolderCIAndMergePassesOverAnAlreadyMergedChild(t *testing.T) {
	const (
		id     = "COD-96423"
		branch = "feature/COD-96423-cross-repo-slice"
	)
	root := t.TempDir()
	gits := map[string]*childGit{"api-billing": {}, "api-companies": {}}
	ghs := map[string]*epicGitHub{
		"api-billing":   {prState: "MERGED"},
		"api-companies": {prState: "OPEN"},
	}
	makeChildRepos(t, root, "api-billing", "api-companies")

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.AutoMerge = true
	p.MergeMethod = "squash"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.GitHubAt = func(path string) GitHub { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"PHASE":        state.PROpen,
		"SHIP_TARGETS": "api-billing,api-companies",
		"PR_URLS":      "api-billing=https://github.com/acme/api-billing/pull/5,api-companies=https://github.com/acme/api-companies/pull/3",
	} {
		mustSet(t, p, id, key, value)
	}

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}

	if ghs["api-billing"].mergeCalls != 0 {
		t.Errorf("merged api-billing's already-merged PR %d time(s), want 0", ghs["api-billing"].mergeCalls)
	}
	if ghs["api-companies"].mergeCalls != 1 {
		t.Errorf("merged api-companies' PR %d time(s), want 1", ghs["api-companies"].mergeCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want %q", got, state.Merged)
	}
}
