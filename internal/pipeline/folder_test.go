package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/proofsbranch"
)

// childGit is one Child repo's git in the fan-out tests: it reports what the
// child's working tree carries and which branch it sits on (empty head meaning
// the base branch), and records what the ship phase did to it.
type childGit struct {
	fakeGit
	status        string
	head          string
	sha           string // what HEAD resolves to, the fork point for a freshly created branch
	forkedAt      string // the merge base MergeBase reports, for a branch that already exists
	branch        string
	commits       []string
	pushed        []string
	deleted       []string
	deletedRemote []string
	checkedOut    []string
	forced        []string // the checkouts that were allowed to discard the tree
	cleaned       int
	hasBranch     bool   // whether the ticket's branch already exists here
	baseBehind    bool   // this child's remote base is missing the local base tip
	pushFixes     bool   // whether pushing the base brings the remote current
	remote        string // the origin URL, which is what says where this child is hosted
	remoteBase    string // the branch this child's own remote calls default
	cutFrom       string // the ref CreateBranch was asked to cut at
}

func (g *childGit) RemoteURL(context.Context, string) string {
	if g.remote == "" {
		return "git@github.com:acme/child.git"
	}
	return g.remote
}

func (g *childGit) RemoteDefaultBranch(context.Context, string) string {
	return g.remoteBase
}

func (g *childGit) WorktreeStatus(context.Context) (string, error) { return g.status, nil }

func (g *childGit) HeadSHA(context.Context) (string, error) { return g.sha, nil }

func (g *childGit) MergeBase(context.Context, string, string) (string, error) {
	return g.forkedAt, nil
}

func (g *childGit) CurrentBranch(ctx context.Context) (string, error) {
	if g.head != "" {
		return g.head, nil
	}
	return g.fakeGit.CurrentBranch(ctx)
}

func (g *childGit) CreateBranch(_ context.Context, branch, base string) error {
	g.branch, g.cutFrom = branch, base
	return nil
}

func (g *childGit) Commit(_ context.Context, message string, _ bool) error {
	g.commits = append(g.commits, message)
	return nil
}

func (g *childGit) Push(_ context.Context, _, ref string, _ bool) error {
	g.pushed = append(g.pushed, ref)
	if ref == prBaseBranch && g.pushFixes {
		g.baseBehind = false
	}
	return nil
}

func (g *childGit) RemoteSHA(context.Context, string, string) (string, error) {
	return prBaseRemoteTip, nil
}

func (g *childGit) Checkout(_ context.Context, ref string, force bool) error {
	g.checkedOut = append(g.checkedOut, ref)
	if force {
		g.forced = append(g.forced, ref)
	}
	return nil
}

func (g *childGit) Clean(context.Context) error { g.cleaned++; return nil }

func (g *childGit) BranchExists(context.Context, string) (bool, error) {
	return g.hasBranch, nil
}

func (g *childGit) RemoteBranchExists(_ context.Context, _, _ string) (bool, error) {
	return g.hasBranch, nil
}

func (g *childGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return nil
}

func (g *childGit) DeletePushedBranch(_ context.Context, _, branch string) error {
	g.deletedRemote = append(g.deletedRemote, branch)
	return nil
}

func (g *childGit) IsAncestor(context.Context, string, string) (bool, error) {
	return !g.baseBehind, nil
}

// TestFolderShipsEachChildToItsOwnBase holds the three facts a folder run has to
// read per child off that child's own remote: api-orders ships to master and
// api-web to main, so a folder that is mostly one branch no longer makes the odd
// one out unshippable; api-web is standing on a spike branch and is moved back
// onto its base rather than skipped for it; and api-legacy is on Azure DevOps,
// where trau opens no pull requests, so it is named off limits before the build
// runs instead of dying at PR time.
func TestFolderShipsEachChildToItsOwnBase(t *testing.T) {
	id := "COD-93020"
	branch := "feature/COD-93020-own-base"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-legacy": {remote: "https://dev.azure.com/acme/platform/_git/api-legacy"},
		"api-orders": {remoteBase: "master"},
		"api-web":    {remoteBase: "main", head: "spike/redesign"},
	}
	ghs := map[string]*epicGitHub{
		"api-legacy": {},
		"api-orders": {createURL: "https://github.com/acme/api-orders/pull/11"},
		"api-web":    {createURL: "https://github.com/acme/api-web/pull/4"},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := p.EnsureCleanBase(ctx); err != nil {
		t.Fatalf("EnsureCleanBase: %v", err)
	}
	p.startFolderRun(ctx, id, false)
	gits["api-orders"].status = " M orders.go"
	gits["api-web"].status = "?? web/panel.tsx"

	if !slices.Contains(gits["api-web"].checkedOut, "main") {
		t.Errorf("api-web checkouts = %v, want it moved onto its own base before the build", gits["api-web"].checkedOut)
	}
	if reason := p.folderOffLimits(ctx)["api-legacy"]; !strings.Contains(reason, "Azure DevOps") {
		t.Errorf("api-legacy off-limits reason = %q, want its forge named", reason)
	}

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	for name, wantBase := range map[string]string{"api-orders": "master", "api-web": "main"} {
		if got := gits[name].cutFrom; got != wantBase {
			t.Errorf("%s branch cut from %q, want %q", name, got, wantBase)
		}
		if got := ghs[name].base; got != wantBase {
			t.Errorf("%s PR base = %q, want %q", name, got, wantBase)
		}
	}
	if got := p.State.Get(id, childBaseKey); !strings.Contains(got, "api-orders=master") || !strings.Contains(got, "api-web=main") {
		t.Errorf("%s = %q, want each child's own base recorded for a resume", childBaseKey, got)
	}
	if ghs["api-legacy"].createCalls > 0 {
		t.Errorf("api-legacy opened %d PRs, want none — trau delivers to GitHub only", ghs["api-legacy"].createCalls)
	}
}

// TestFolderShipFansOutToEveryChangedChild is the Folder repo delivery contract:
// the ticket's branch is cut — with the same name — in each Child repo the build
// changed and in no other, each gets its own PR, and the checkpoint records the
// whole ship set. api-billing carries everything an abandoned earlier attempt at
// this ticket left: the branch, a recorded ship target and its PR. Branch names and
// checkpoint keys belong to the ticket, not to the attempt, so a fresh run must
// read none of it as its own work.
func TestFolderShipFansOutToEveryChangedChild(t *testing.T) {
	id := "COD-93010"
	branch := "feature/COD-93010-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-apigateway": {},
		"api-companies":  {},
		"api-billing":    {hasBranch: true},
	}
	ghs := map[string]*epicGitHub{
		"api-apigateway": {createURL: "https://github.com/acme/api-apigateway/pull/7"},
		"api-companies":  {createURL: "https://github.com/acme/api-companies/pull/3"},
		"api-billing":    {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"SHIP_TARGETS": "api-billing",
		"PR_URLS":      "api-billing=https://github.com/acme/api-billing/pull/5",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-apigateway"].status = " M gateway.go"
	gits["api-companies"].status = "?? companies/client.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	for _, name := range []string{"api-apigateway", "api-companies"} {
		g := gits[name]
		if g.branch != branch {
			t.Errorf("%s branch = %q, want %q", name, g.branch, branch)
		}
		if len(g.commits) != 1 {
			t.Errorf("%s commits = %v, want exactly one", name, g.commits)
		}
		if len(g.pushed) != 1 || g.pushed[0] != branch {
			t.Errorf("%s pushed = %v, want [%s]", name, g.pushed, branch)
		}
		if ghs[name].createCalls != 1 {
			t.Errorf("%s opened %d PRs, want 1", name, ghs[name].createCalls)
		}
	}

	untouched := gits["api-billing"]
	if untouched.branch != "" || len(untouched.commits) > 0 || len(untouched.pushed) > 0 || ghs["api-billing"].createCalls > 0 {
		t.Errorf("api-billing was changed by nothing but got branch %q, commits %v, pushes %v, %d PRs",
			untouched.branch, untouched.commits, untouched.pushed, ghs["api-billing"].createCalls)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-apigateway,api-companies" {
		t.Errorf("SHIP_TARGETS = %q, want the two changed children", got)
	}
	urls := p.State.Get(id, "PR_URLS")
	for _, want := range []string{"api-apigateway=" + ghs["api-apigateway"].createURL, "api-companies=" + ghs["api-companies"].createURL} {
		if !strings.Contains(urls, want) {
			t.Errorf("PR_URLS = %q, want it to carry %q", urls, want)
		}
	}
	if got := p.State.Get(id, "PR_URL"); got != ghs["api-apigateway"].createURL {
		t.Errorf("PR_URL = %q, want the first target's PR", got)
	}
}

// TestFolderShipLeavesChildrenTheRunFoundDirty is the off-limits contract at ship
// time: a child an operator left uncommitted work in — an untracked file is work —
// is named off limits and then left exactly as it was found. Only the build's own
// change makes a child a ship target, so the untouched child gets no branch, no
// commit and no PR, and its dirt does not give the run up either.
func TestFolderShipLeavesChildrenTheRunFoundDirty(t *testing.T) {
	id := "COD-93012"
	branch := "feature/COD-93012-one-child-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-a": {},
		"api-b": {},
		"api-c": {status: "?? scratch.md", head: "wip/local"},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/4"},
		"api-b": {},
		"api-c": {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-a"].status = " M handler.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a" {
		t.Errorf("SHIP_TARGETS = %q, want only the child the build changed", got)
	}
	if got := p.State.Get(id, offLimitsKey); !strings.Contains(got, "api-c=it has uncommitted changes") {
		t.Errorf("%s = %q, want api-c named for the work it already carried", offLimitsKey, got)
	}
	stray := gits["api-c"]
	if stray.branch != "" || len(stray.commits) > 0 || len(stray.pushed) > 0 || ghs["api-c"].createCalls > 0 {
		t.Errorf("api-c was dirty before the run and untouched by it, but got branch %q, commits %v, pushes %v, %d PRs",
			stray.branch, stray.commits, stray.pushed, ghs["api-c"].createCalls)
	}
}

// TestFolderShipFailsOnlyTheChildItCannotCommit is the per-child grain of the
// commit phase: a child whose .gitconfig.repo git cannot read fails on its own,
// its siblings are still committed and recorded as ship targets, and the run is
// given up naming only the child that could not be committed in.
func TestFolderShipFailsOnlyTheChildItCannotCommit(t *testing.T) {
	id := "COD-93016"
	branch := "feature/COD-93016-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-billing": {},
		"api-users":   {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "api-users", RepoConfigFile), []byte("this is not a git config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-billing"].status = " M billing.go"
	gits["api-users"].status = " M users.go"

	err := p.CommitAndPR(ctx, id)
	give := AsGiveUp(err)
	if give == nil {
		t.Fatalf("CommitAndPR = %v, want a give-up naming the child it could not commit in", err)
	}
	if !strings.Contains(give.Reason, "api-users") || strings.Contains(give.Reason, "api-billing") {
		t.Errorf("give-up reason = %q, want only api-users named", give.Reason)
	}
	if len(gits["api-billing"].commits) != 1 {
		t.Errorf("api-billing commits = %v, want its sibling's failure to have left it shipped", gits["api-billing"].commits)
	}
	if len(gits["api-users"].commits) > 0 {
		t.Errorf("api-users commits = %v, want none — its identity could not be wired", gits["api-users"].commits)
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-billing" {
		t.Errorf("SHIP_TARGETS = %q, want the child that did commit, so its branch is not left unrecorded", got)
	}
}

// TestFolderResumeKeepsTheStartOfRunSweep is the resume contract: the off-limits
// census a run is judged against is the one taken before its build ran, so a run
// picked up from a checkpoint ships the children its own build dirtied instead of
// reading them back as off limits and giving up.
func TestFolderResumeKeepsTheStartOfRunSweep(t *testing.T) {
	id := "COD-93011"
	branch := "feature/COD-93011-resumed-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-a": {},
		"api-b": {},
	}
	ghs := map[string]*epicGitHub{
		"api-a": {createURL: "https://github.com/acme/api-a/pull/9"},
		"api-b": {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)

	// What the interrupted build left behind is exactly what a second sweep would
	// read as an unclean child.
	gits["api-a"].status = " M service.go"
	p.resetFolderRun()
	p.startFolderRun(ctx, id, true)

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR after resume: %v", err)
	}
	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-a" {
		t.Errorf("SHIP_TARGETS = %q, want the child the build changed", got)
	}
	if ghs["api-a"].createCalls != 1 {
		t.Errorf("api-a opened %d PRs, want 1", ghs["api-a"].createCalls)
	}
}

// TestFolderShipResumesEveryChildAnInterruptedRunLeftBehind is the crash-safety
// contract of the fan-out: a run killed mid-ship left api-companies committed and
// recorded, api-billing committed before its stamp landed, and api-users still
// carrying loose work. The resumed run ships all three — the recorded set, the
// branch probe and the dirty tree each name one of them — commits nothing twice,
// and gives every one of them its own fork point: the recorded pin where there is
// one, and the branch's merge base for the child whose stamp never landed.
func TestFolderShipResumesEveryChildAnInterruptedRunLeftBehind(t *testing.T) {
	id := "COD-93017"
	branch := "feature/COD-93017-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-billing":   {hasBranch: true, forkedAt: "billing-cut-at"},
		"api-companies": {hasBranch: true, forkedAt: "companies-drifted-to"},
		"api-idle":      {},
		"api-users":     {status: " M users.go", sha: "users-cut-at"},
	}
	ghs := map[string]*epicGitHub{
		"api-billing":   {createURL: "https://github.com/acme/api-billing/pull/5"},
		"api-companies": {createURL: "https://github.com/acme/api-companies/pull/3"},
		"api-idle":      {},
		"api-users":     {createURL: "https://github.com/acme/api-users/pull/8"},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"SHIP_TARGETS": "api-companies",
		"FORK_POINTS":  "api-companies=companies-cut-at",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, true)

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR after resume: %v", err)
	}

	if got := p.State.Get(id, "SHIP_TARGETS"); got != "api-billing,api-companies,api-users" {
		t.Errorf("SHIP_TARGETS = %q, want every child the interrupted run left work in", got)
	}
	want := "api-billing=billing-cut-at,api-companies=companies-cut-at,api-users=users-cut-at"
	if got := p.State.Get(id, "FORK_POINTS"); got != want {
		t.Errorf("FORK_POINTS = %q, want every shipped child pinned: %q", got, want)
	}
	for _, name := range []string{"api-billing", "api-companies"} {
		if g := gits[name]; len(g.commits) > 0 {
			t.Errorf("%s committed %v, want nothing — its work was already on the branch", name, g.commits)
		}
	}
	if g := gits["api-users"]; len(g.commits) != 1 || g.branch != branch {
		t.Errorf("api-users committed %v on branch %q, want one commit on %s", g.commits, g.branch, branch)
	}
	for _, name := range []string{"api-billing", "api-companies", "api-users"} {
		if ghs[name].createCalls != 1 {
			t.Errorf("%s opened %d PRs, want 1", name, ghs[name].createCalls)
		}
	}
	idle := gits["api-idle"]
	if idle.branch != "" || len(idle.commits) > 0 || ghs["api-idle"].createCalls > 0 {
		t.Errorf("api-idle held none of the run's work but got branch %q, commits %v, %d PRs",
			idle.branch, idle.commits, ghs["api-idle"].createCalls)
	}
}

// TestFolderShipCrossLinksEveryPRAndPublishesProofs is the reviewability contract:
// the verify proofs land in the first changed child and every PR body embeds that
// one gallery, then a second pass rewrites each body with the siblings' URLs —
// which no first pass can carry, since a PR's URL exists only once gh created it.
func TestFolderShipCrossLinksEveryPRAndPublishesProofs(t *testing.T) {
	id := "COD-93018"
	branch := "feature/COD-93018-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-companies": {},
		"api-users":     {},
	}
	ghs := map[string]*epicGitHub{
		"api-companies": {createURL: "https://github.com/acme/api-companies/pull/3"},
		"api-users":     {createURL: "https://github.com/acme/api-users/pull/8"},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	publishedTo := ""
	p.PublishProofs = func(_ context.Context, repoDir, _ string) (proofsbranch.Publication, error) {
		publishedTo = repoDir
		return proofsbranch.Publication{
			Owner:  "acme",
			Repo:   "api-companies",
			Branch: proofsbranch.Branch,
			Files:  []proofsbranch.File{{Path: id + "/proof-1.png", Caption: "home"}},
		}, nil
	}
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-companies"].status = " M companies.go"
	gits["api-users"].status = " M users.go"

	if err := p.CommitAndPR(ctx, id); err != nil {
		t.Fatalf("CommitAndPR: %v", err)
	}

	if want := filepath.Join(root, "api-companies"); publishedTo != want {
		t.Errorf("proofs published to %q, want the first changed child %q", publishedTo, want)
	}
	for name, gh := range ghs {
		if !strings.Contains(gh.body, "### QA proofs") {
			t.Errorf("%s PR body carries no QA proofs section:\n%s", name, gh.body)
		}
	}
	links := map[string]string{
		"3": "api-users: " + ghs["api-users"].createURL,
		"8": "api-companies: " + ghs["api-companies"].createURL,
	}
	for name, gh := range ghs {
		pr := prNumber(gh.createURL)
		edited, ok := gh.bodyEdits[pr]
		if !ok {
			t.Fatalf("%s PR #%s body was never rewritten with its siblings", name, pr)
		}
		if !strings.Contains(edited, links[pr]) {
			t.Errorf("%s PR body must name its sibling %q:\n%s", name, links[pr], edited)
		}
		if strings.Contains(edited, "- "+name+":") {
			t.Errorf("%s PR body lists itself among its siblings:\n%s", name, edited)
		}
	}
}

// TestFolderResetDropsShipTargetsAndSparesUnrelatedWIP is the Folder repo undo
// contract: every recorded ship target loses its branch on both sides and its
// working tree, and a child the run never shipped to is left exactly as it was
// found — whether its work was already there when the run started or an operator
// added it while the run was in flight. api-companies ships to master, so the undo
// puts it back on its own base rather than on the folder's.
func TestFolderResetDropsShipTargetsAndSparesUnrelatedWIP(t *testing.T) {
	id := "COD-93014"
	branch := "feature/COD-93014-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-companies": {hasBranch: true, remoteBase: "master"},
		"api-billing":   {status: " M billing.go"},
		"api-users":     {},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }

	ctx := context.Background()
	p.startFolderRun(ctx, id, false)
	gits["api-companies"].status = " M companies.go"
	gits["api-users"].status = "?? midrun.txt"
	for key, value := range map[string]string{"BRANCH": branch, "PHASE": "building", "SHIP_TARGETS": "api-companies"} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	p.resetLocal(ctx, id)

	shipped := gits["api-companies"]
	if !slices.Contains(shipped.deleted, branch) || !slices.Contains(shipped.deletedRemote, branch) {
		t.Errorf("api-companies deleted %v locally and %v on the remote, want %s in both", shipped.deleted, shipped.deletedRemote, branch)
	}
	if !slices.Contains(shipped.checkedOut, "master") || slices.Contains(shipped.checkedOut, p.Base) || shipped.cleaned == 0 {
		t.Errorf("api-companies checked out %v and cleaned %d times, want it back on its own base master, never on the folder's %s", shipped.checkedOut, shipped.cleaned, p.Base)
	}
	wip := gits["api-billing"]
	if len(wip.deleted) > 0 || len(wip.checkedOut) > 0 || wip.cleaned > 0 {
		t.Errorf("api-billing carried WIP the run never touched but was reset: deleted %v, checked out %v, cleaned %d times", wip.deleted, wip.checkedOut, wip.cleaned)
	}
	if midrun := gits["api-users"]; len(midrun.checkedOut) > 0 || midrun.cleaned > 0 {
		t.Errorf("api-users was dirtied after the run shipped elsewhere but got checkouts %v and %d cleans", midrun.checkedOut, midrun.cleaned)
	}
}

// TestFolderPurgeLocalDropsEveryBranchAndKeepsChildWork is the Folder repo purge
// contract: a hard-deleted ticket loses its branch in every recorded ship target,
// on both sides, and nothing a child still holds uncommitted goes with it — a
// purge undoes the ticket, not the working trees a reset is asked to clear.
func TestFolderPurgeLocalDropsEveryBranchAndKeepsChildWork(t *testing.T) {
	id := "COD-93015"
	branch := "feature/COD-93015-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-companies": {hasBranch: true, status: " M companies.go"},
		"api-billing":   {hasBranch: true},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	for key, value := range map[string]string{"BRANCH": branch, "SHIP_TARGETS": "api-companies,api-billing"} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.PurgeLocal(context.Background(), id); err != nil {
		t.Fatalf("PurgeLocal: %v", err)
	}

	for name, g := range gits {
		if !slices.Contains(g.deleted, branch) || !slices.Contains(g.deletedRemote, branch) {
			t.Errorf("%s deleted %v locally and %v on the remote, want %s in both", name, g.deleted, g.deletedRemote, branch)
		}
		if len(g.forced) > 0 || g.cleaned > 0 {
			t.Errorf("%s was force-checked-out %v and cleaned %d times, want whatever it carries left alone", name, g.forced, g.cleaned)
		}
	}
}

// TestFolderRequeueClosesEveryPRTheRunOpened is the Folder repo requeue contract:
// a run that fanned out over several children closes all of its pull requests and
// drops all of its branches, not just the first target's.
func TestFolderRequeueClosesEveryPRTheRunOpened(t *testing.T) {
	id := "COD-93013"
	branch := "feature/COD-93013-cross-repo-slice"
	root := t.TempDir()
	gits := map[string]*childGit{
		"api-apigateway": {hasBranch: true},
		"api-companies":  {hasBranch: true},
	}
	ghs := map[string]*epicGitHub{
		"api-apigateway": {prState: "OPEN"},
		"api-companies":  {prState: "OPEN"},
	}
	for name := range gits {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.FolderRepo = true
	p.RepoRoot = root
	p.Remote = "origin"
	p.GitAt = func(path string) Git { return gits[filepath.Base(path)] }
	p.DeliveryAt = func(path string) Delivery { return ghs[filepath.Base(path)] }
	for key, value := range map[string]string{
		"BRANCH":       branch,
		"PHASE":        "pr_open",
		"SHIP_TARGETS": "api-apigateway,api-companies",
		"PR_URLS":      "api-apigateway=https://github.com/acme/api-apigateway/pull/7,api-companies=https://github.com/acme/api-companies/pull/3",
	} {
		if err := p.State.Set(id, key, value); err != nil {
			t.Fatal(err)
		}
	}

	if err := p.Requeue(context.Background(), id, false); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	for name, pr := range map[string]string{"api-apigateway": "7", "api-companies": "3"} {
		if ghs[name].closedPR != pr {
			t.Errorf("%s closed PR %q, want %q", name, ghs[name].closedPR, pr)
		}
		if !slices.Contains(gits[name].deleted, branch) {
			t.Errorf("%s deleted branches %v, want %s among them", name, gits[name].deleted, branch)
		}
		if !slices.Contains(gits[name].deletedRemote, branch) {
			t.Errorf("%s deleted remote branches %v, want %s among them", name, gits[name].deletedRemote, branch)
		}
	}
}
