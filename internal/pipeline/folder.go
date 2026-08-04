package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/checks"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/folderrepo"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// offLimitsKey, startDirtKey and childBaseKey carry the start-of-run sweep on the
// ticket's checkpoint — which children may not be changed, how dirty each one
// already was, and the base each one ships to — so a resumed run judges and
// branches its children exactly as the run that started them did, instead of
// re-reading against the build's own leftovers.
const (
	offLimitsKey = "OFF_LIMITS"
	startDirtKey = "START_DIRT"
	childBaseKey = "CHILD_BASES"
)

// childDirt fingerprints a Child repo's uncommitted work, untracked files
// included. Both the start-of-run sweep and the changed-children check read it,
// so a stray file an operator left behind cannot read as clean to one and as this
// run's work to the other.
func childDirt(ctx context.Context, g Git, dir string) (string, error) {
	status, err := g.WorktreeStatus(ctx)
	if err != nil {
		return "", err
	}
	return folderrepo.Fingerprint(dir, status), nil
}

// shipTarget is a Child repo a run changed, with the PR its branch carries and
// the commit that branch was cut at.
type shipTarget struct {
	folderrepo.Child
	PRURL string
	Fork  string
}

// attempt is one repo a run's undo has to act in: the target repo itself on a
// plain Repo, one per recorded ship target in a Folder repo, each with the PR its
// branch carries. Reset, PurgeLocal and Requeue all act on the whole set, so a
// folder run's every branch and every PR is undone rather than only the first.
type attempt struct {
	repo   string // Child repo name, empty for the target repo itself
	git    Git
	github GitHub
	pr     string
}

// inRepo names the Child repo an attempt acted in as a message suffix — " in
// api-billing" — for a message that has to say which of several it means. The
// target repo itself needs no naming and contributes nothing.
func (a attempt) inRepo() string {
	if a.repo == "" {
		return ""
	}
	return " in " + a.repo
}

func (p *Pipeline) attempts(id string) []attempt {
	if !p.FolderRepo {
		return []attempt{{git: p.Git, github: p.GitHub, pr: p.State.Get(id, "PR")}}
	}
	targets := p.shipTargets(id)
	out := make([]attempt, 0, len(targets))
	for _, t := range targets {
		out = append(out, attempt{
			repo:   t.Name,
			git:    p.childGit(t.Child),
			github: p.childGitHub(t.Child),
			pr:     prNumber(t.PRURL),
		})
	}
	return out
}

// baseCheckout puts an attempt's repo back on the base branch before its branch
// is deleted. Only the target repo's own checkout can collide with another
// worktree holding the base, so only it goes through checkoutBase. discard says
// what a Child repo's uncommitted work is up against: a reset throws it away on
// purpose, while a purge leaves it standing and lets the branch it could not
// leave be reported instead.
func (p *Pipeline) baseCheckout(ctx context.Context, a attempt, discard bool) error {
	if a.repo == "" {
		_, err := p.checkoutBase(ctx, true)
		return err
	}
	return a.git.Checkout(ctx, p.baseFor(a.repo), discard)
}

// RefuseEpic answers ErrFolderRepoEpic when a Folder repo is asked to run the epic
// itself. Entry points call it before they bind EpicID so the refusal lands before
// the run starts rather than on whichever sub-issue the epic's pick happens to
// offer first. A leaf that merely belongs to an epic is not an epic run: in a
// Folder repo it is built off each child's own base — the individual sub-issue run
// the refusal points operators at — so those callers never bind EpicID at all.
func (p *Pipeline) RefuseEpic(epic string) error {
	if p.FolderRepo && epic != "" {
		return ErrFolderRepoEpic
	}
	return nil
}

// resetFolderRun drops the folder scan and sweep a previous ticket cached, so a
// long-lived Pipeline re-reads the folder for every run.
func (p *Pipeline) resetFolderRun() {
	p.children = nil
	p.offLimits = nil
	p.startDirt = nil
	p.childBases = nil
	p.parked = nil
	p.sliceChecks = nil
	p.folderResumed = false
}

func (p *Pipeline) childGit(c folderrepo.Child) Git {
	if p.GitAt != nil {
		return p.GitAt(c.Path)
	}
	return ExecGit{Repo: c.Path}
}

func (p *Pipeline) childGitHub(c folderrepo.Child) GitHub {
	if p.GitHubAt != nil {
		return p.GitHubAt(c.Path)
	}
	return ExecGitHub{Repo: c.Path}
}

// folderChildren is the run's Child repo set, scanned once. A folder that cannot
// be read yields none, which every caller treats as nothing to ship.
func (p *Pipeline) folderChildren() []folderrepo.Child {
	if p.children != nil {
		return p.children
	}
	children, truncated, err := folderrepo.Children(p.RepoRoot)
	if err != nil {
		p.logf("  ⚠ could not scan %s for child repos: %v", p.repoLabel(), err)
		return nil
	}
	if truncated {
		p.logf("  ⚠ %s holds more than %d git repositories — only the first %d are ship targets", p.repoLabel(), folderrepo.MaxChildren, folderrepo.MaxChildren)
	}
	p.children = children
	return children
}

// startFolderRun settles the off-limits census the whole run is judged against. A
// fresh run sweeps the children and stamps the verdict on the checkpoint; a resumed
// run reads that verdict back, because the build it is picking up has since left its
// own work in the children it changed and a second sweep would call every one of
// them off limits.
func (p *Pipeline) startFolderRun(ctx context.Context, id string, resumed bool) {
	p.folderResumed = resumed
	if resumed {
		p.offLimits = folderrepo.ParseCensus(p.State.Get(id, offLimitsKey))
		p.startDirt = folderrepo.ParseCensus(p.State.Get(id, startDirtKey))
		p.childBases = folderrepo.ParseCensus(p.State.Get(id, childBaseKey))
		return
	}
	p.sweepChildren(ctx)
	p.parkChildrenOnBase(ctx)
	p.recordCensus(id, offLimitsKey, p.offLimits)
	p.recordCensus(id, startDirtKey, p.startDirt)
	p.recordCensus(id, childBaseKey, p.childBases)
}

// parkChildrenOnBase puts every clean child standing somewhere else back on its
// own base — what EnsureCleanBase does for a plain Repo, and what a folder run
// used to skip the child over instead. It runs where the census is settled and
// before the build, so a checkout git refuses puts that child off limits on the
// checkpoint rather than letting a build run off the wrong branch.
func (p *Pipeline) parkChildrenOnBase(ctx context.Context) {
	for _, name := range folderrepo.SortedNames(p.parked) {
		c := folderrepo.Child{Name: name, Path: filepath.Join(p.RepoRoot, name)}
		base := p.baseFor(name)
		if err := p.childGit(c).Checkout(ctx, base, false); err != nil {
			p.offLimits[name] = "it sits on " + p.parked[name] + " and could not be moved onto " + base
			p.logf("  ⚠ %s: off limits — %v", name, err)
			continue
		}
		p.logf("  ↻ %s: moved from %s onto %s", name, p.parked[name], base)
	}
}

// baseFor is the branch a Child repo is cut from and ships back to: its own, as
// the start-of-run sweep settled it, so a folder that is mostly master with one
// repo on main ships to both instead of leaving the odd one out unshippable. The
// folder's own base stands in for a child the sweep never reached.
func (p *Pipeline) baseFor(child string) string {
	if base := p.childBases[child]; base != "" {
		return base
	}
	return p.Base
}

func (p *Pipeline) recordCensus(id, key string, census map[string]string) {
	if err := p.State.Set(id, key, folderrepo.FormatCensus(census)); err != nil {
		p.logf("  ⚠ could not record %s for %s: %v", key, id, err)
	}
}

// sweepChildren takes the census the whole run is judged against: which Child
// repos are off limits and why, what uncommitted work each one already held, the
// base each one ships to, and which of them a run moves back onto that base.
// The reads are per child and cheap — a status, a HEAD, the remote's URL and, for
// a clone that never recorded origin/HEAD, one ls-remote — deliberately in place
// of the fetches and checkouts that preparing every child would cost. It judges
// only: a child lands off limits so the build agent is told to stay out, and
// moving a parked child onto its base is sweepFolder's job, not this one's.
func (p *Pipeline) sweepChildren(ctx context.Context) {
	if p.offLimits != nil {
		return
	}
	off, dirt := map[string]string{}, map[string]string{}
	bases, parked := map[string]string{}, map[string]string{}
	for _, st := range folderrepo.Sweep(ctx, p.folderChildren(), p.readChildState) {
		bases[st.Name] = st.Base
		if st.Dirt != "" {
			dirt[st.Name] = st.Dirt
		}
		if reason := st.OffLimitsReason(); reason != "" {
			off[st.Name] = reason
		}
		if st.ParkedAndMovable() {
			parked[st.Name] = st.Branch
		}
	}
	p.offLimits, p.startDirt, p.childBases, p.parked = off, dirt, bases, parked
}

// folderOffLimits names the Child repos this run must not change, mapped to why.
func (p *Pipeline) folderOffLimits(ctx context.Context) map[string]string {
	p.sweepChildren(ctx)
	return p.offLimits
}

// folderStartDirt fingerprints, per Child repo, the uncommitted work the run
// found there before its build ran. Children the sweep read as clean are absent.
func (p *Pipeline) folderStartDirt(ctx context.Context) map[string]string {
	p.sweepChildren(ctx)
	return p.startDirt
}

func (p *Pipeline) readChildState(ctx context.Context, c folderrepo.Child) folderrepo.State {
	g := p.childGit(c)
	st := folderrepo.State{Child: c}
	branch, err := g.CurrentBranch(ctx)
	if err != nil {
		st.Err = err
		return st
	}
	st.Branch = branch
	st.Base = p.baseOf(ctx, g, config.Declared(c.Path, "BASE_BRANCH"))
	st.Forge = p.forgeOf(ctx, g, config.Declared(c.Path, "FORGE"))
	if st.Dirt, err = childDirt(ctx, g, c.Path); err != nil {
		st.Err = err
	}
	return st
}

// sweepFolder is the Folder repo's stand-in for EnsureCleanBase: it takes the
// cheap census of the children and reports what it found. Nothing is fetched or
// stashed, and the clean children standing off their base are moved back onto it
// where the census is stamped on the checkpoint (see startFolderRun); a run
// touches a child otherwise for the first time when it cuts that child's branch
// at ship time.
func (p *Pipeline) sweepFolder(ctx context.Context) error {
	children := p.folderChildren()
	if len(children) == 0 {
		return fmt.Errorf("folder repo %s holds no git repositories to work in", p.repoLabel())
	}
	off := p.folderOffLimits(ctx)
	p.logf("  ⓘ %s: %d child repos, %d ready", p.repoLabel(), len(children), len(children)-len(off))
	for _, name := range folderrepo.SortedNames(off) {
		p.logf("      off limits: %s — %s", name, off[name])
	}
	return nil
}

// changedChildren lists the Child repos the build actually touched: the ones
// whose working tree no longer reads as the start-of-run sweep found it. A folder
// run commits nothing until ship — the branch it would have committed to is no
// part of the reading — so the change is still loose in the tree, but so is
// whatever an operator left there, and that is theirs, not this run's.
func (p *Pipeline) changedChildren(ctx context.Context) []folderrepo.Child {
	return folderrepo.Carrying(ctx, p.folderChildren(), p.folderStartDirt(ctx), "", p.readChildState)
}

// folderBuildNote tells the build agent it is working across a folder of
// repositories, names them, and names the ones the sweep put off limits.
func (p *Pipeline) folderBuildNote(ctx context.Context) string {
	names := make([]string, 0, len(p.folderChildren()))
	for _, c := range p.folderChildren() {
		names = append(names, c.Name)
	}
	var b strings.Builder
	b.WriteString("\n\nThis repo is a folder of git repositories, not a repository itself. Its child repositories are: ")
	b.WriteString(strings.Join(names, ", "))
	b.WriteString(".\nWork inside whichever of them the ticket needs — every child you change gets its own branch and its own pull request, all named the same. Do not create a repository, and do not put files in the folder root itself.")
	off := p.folderOffLimits(ctx)
	if len(off) == 0 {
		return b.String()
	}
	b.WriteString("\n\nOff limits — leave every file in these untouched, they were not clean when the run started:\n")
	for _, name := range folderrepo.SortedNames(off) {
		b.WriteString("- " + name + " (" + off[name] + ")\n")
	}
	return b.String()
}

// assertChildrenInBounds aborts a run whose build landed in a Child repo the
// sweep put off limits. Its change is entangled with work trau does not own, so
// nothing is committed anywhere and a human separates them.
func (p *Pipeline) assertChildrenInBounds(ctx context.Context, id string) error {
	off := p.folderOffLimits(ctx)
	hit := []string{}
	for _, c := range p.changedChildren(ctx) {
		if reason, ok := off[c.Name]; ok {
			hit = append(hit, c.Name+" ("+reason+")")
		}
	}
	if len(hit) == 0 {
		return nil
	}
	return &GiveUpError{ID: id, Reason: "the build changed " + strings.Join(hit, ", ") + ", which the start-of-run sweep named off limits"}
}

// assertFolderChanged is the Folder repo's empty-diff guard: the build must have
// left work in at least one child.
func (p *Pipeline) assertFolderChanged(ctx context.Context) error {
	if len(p.changedChildren(ctx)) > 0 {
		return nil
	}
	return fmt.Errorf("build produced no changes in any child repo of %s — the agent may have built in the wrong repository or escaped its working directory", p.repoLabel())
}

// folderLintFix runs each changed Child repo's own lint-fix command inside it,
// falling back to the folder root's — the ADR 0019 workspace grain with the child
// repo as the workspace. With no command configured anywhere the agent fixer runs
// once over the whole folder rather than once per child.
func (p *Pipeline) folderLintFix(ctx context.Context, id string) error {
	ran := false
	for _, c := range p.changedChildren(ctx) {
		cmd := p.LintFixCmd
		if v, ok := config.WorkspaceOverride(c.Path, "LINT_FIX_CMD"); ok {
			cmd = v
		}
		if strings.TrimSpace(cmd) == "" {
			continue
		}
		ran = true
		if err := p.lintFixCmd(ctx, c.Path, "lint-fix "+c.Name, cmd); err != nil {
			return err
		}
	}
	if ran {
		return nil
	}
	return p.lintFixAgent(ctx, id)
}

// folderChecks is the verify library for a Folder repo slice: each changed Child
// repo's own .trau/checks, with the folder root's as the fallback for a child
// that declares none. Check names are unique across the library, so a check two
// children share is rendered once.
func (p *Pipeline) folderChecks(ctx context.Context) []checks.Check {
	if len(p.Checks) == 0 {
		return nil
	}
	changed := p.changedChildren(ctx)
	if len(changed) == 0 {
		return p.Checks
	}
	seen := make(map[string]bool, len(p.Checks))
	library := make([]checks.Check, 0, len(p.Checks))
	for _, c := range changed {
		own, defaulted, err := checks.Load(c.Path)
		if err != nil || defaulted {
			own = p.Checks
		}
		for _, check := range own {
			if seen[check.Name] {
				continue
			}
			seen[check.Name] = true
			library = append(library, check)
		}
	}
	return library
}

// checksLibrary is the verify library the current slice is gated on: the repo's
// own on a plain Repo, the changed children's on a Folder repo.
func (p *Pipeline) checksLibrary() []checks.Check {
	if p.sliceChecks != nil {
		return p.sliceChecks
	}
	return p.Checks
}

// folderShip is the Folder repo's commit and PR phase. The branch is cut lazily —
// only in the children the build actually changed, and with the same name in each
// — so a run never touches a checkout the ticket had no business in. A child whose
// commit cannot be made — an unreadable .gitconfig.repo is the way that happens —
// fails on its own: its siblings are still committed and recorded as ship targets,
// so nothing this run cut is left outside SHIP_TARGETS, and the run is then given
// up naming the children it could not commit in. Nothing is pushed: the cross-repo
// change is incomplete, and no part of it may reach a remote or a merge.
//
// The checkpoint is stamped after every child rather than after the loop, on both
// passes: a run killed partway through has committed branches and opened pull
// requests that only SHIP_TARGETS and PR_URLS can lead anything back to.
func (p *Pipeline) folderShip(ctx context.Context, id string) error {
	p.setActivity(id, activity.Commit, "")
	if err := p.assertChildrenInBounds(ctx, id); err != nil {
		return err
	}
	branch := p.State.Get(id, "BRANCH")
	targets := p.folderShipSet(ctx, id, branch)
	if len(targets) == 0 {
		return fmt.Errorf("commit %s: no child repo of %s carries any change", id, p.repoLabel())
	}
	message := deterministicCommitMessage(id, p.commitTitle(ctx, id))
	shipped := make([]shipTarget, 0, len(targets))
	refused := []string{}
	for _, t := range targets {
		fork, err := p.commitChild(ctx, t.Child, branch, message, t.Fork)
		if err != nil {
			p.logf("  ✗ %s: %v", t.Name, err)
			refused = append(refused, t.Name+" ("+err.Error()+")")
			continue
		}
		t.Fork = fork
		shipped = append(shipped, t)
		if err := p.recordShipTargets(id, shipped); err != nil {
			return err
		}
	}
	if len(refused) > 0 {
		return &GiveUpError{ID: id, Reason: "could not commit " + id + " in " + strings.Join(refused, ", ")}
	}
	if p.localDelivery(ctx) {
		return p.recordLocalDelivery(ctx, id)
	}

	p.setActivity(id, activity.PR, "")
	title := p.slicePRTitle(ctx, id, p.Base, branch)
	body := p.prBody(ctx, id, p.proofsSection(ctx, id, shipped[0].Path))
	for i, t := range shipped {
		if t.PRURL != "" {
			continue
		}
		url, err := p.openChildPR(ctx, t.Child, branch, title, body)
		if err != nil {
			return fmt.Errorf("commit %s in %s: %w", id, t.Name, err)
		}
		shipped[i].PRURL = url
		p.logf("  PR %s", url)
		p.emitEvent("pr_open", map[string]any{"number": prNumberInt(url), "url": url, "repo": t.Name})
		if err := p.recordShipTargets(id, shipped); err != nil {
			return err
		}
	}
	p.crossLinkPRs(ctx, shipped, body)
	if err := p.State.Set(id, "PR", prNumber(shipped[0].PRURL)); err != nil {
		return fmt.Errorf("commit %s: record PR: %w", id, err)
	}
	if err := p.State.Set(id, "PR_URL", shipped[0].PRURL); err != nil {
		return fmt.Errorf("commit %s: record PR_URL: %w", id, err)
	}
	if err := p.setPhase(id, state.PROpen); err != nil {
		return fmt.Errorf("commit %s: checkpoint pr_open: %w", id, err)
	}
	note := "Attach these PR links to the issue: " + strings.Join(targetURLs(shipped), ", ") + "."
	if err := p.Tracker.SetStatus(ctx, id, tracker.StageInReview, note); err != nil {
		p.logf("  status (In Review) error: %v", err)
	}
	return nil
}

// folderShipSet is the set of Child repos ship works through, in name order: the
// ones still carrying the build's loose work, and — on a resume — every one this
// ticket's interrupted attempt recorded plus every one holding its branch. A
// resume needs both of those readings, because a child committed just before the
// run died reads clean again and dropping it there is how half a cross-repo change
// ships. A fresh run gets neither: the checkpoint keys and the branch name are the
// ticket's, not this attempt's, so a child an abandoned earlier run left work in
// must not be branched, pushed and PR'd by a run that never touched it.
func (p *Pipeline) folderShipSet(ctx context.Context, id, branch string) []shipTarget {
	recorded := map[string]shipTarget{}
	if p.folderResumed {
		for _, t := range p.shipTargets(id) {
			recorded[t.Name] = t
		}
	}
	changed := map[string]bool{}
	for _, c := range p.changedChildren(ctx) {
		changed[c.Name] = true
	}
	targets := []shipTarget{}
	for _, c := range p.folderChildren() {
		t, known := recorded[c.Name]
		if !known && !changed[c.Name] && !p.childHoldsBranch(ctx, c, branch) {
			continue
		}
		t.Child = c
		targets = append(targets, t)
	}
	return targets
}

func (p *Pipeline) childHoldsBranch(ctx context.Context, c folderrepo.Child, branch string) bool {
	if !p.folderResumed || branch == "" {
		return false
	}
	exists, _ := p.childGit(c).BranchExists(ctx, branch)
	return exists
}

// commitChild cuts the ticket's branch in one changed Child repo and records the
// slice on it, returning the commit the branch was cut at. The branch is created
// here and not at build time: a child the ticket never reached is never left
// holding an empty branch. The fork point is per child because a folder's children
// have as many bases as it has repositories, and each one advances on its own while
// a long run works the others — anchoring them all at one commit would report a
// sibling's merges as this run's work. A child a previous attempt already committed
// keeps its branch, its fork point and its tree exactly as they are; a pin that
// attempt never got as far as recording is recovered from the branch's merge base,
// so no shipped child is left reading its diff against wherever its base has moved.
func (p *Pipeline) commitChild(ctx context.Context, c folderrepo.Child, branch, message, fork string) (string, error) {
	if _, err := EnsureChildConfigInclude(ctx, p.RepoRoot, c.Path); err != nil {
		return "", fmt.Errorf("wire %s: %w", RepoConfigFile, err)
	}
	g, base := p.childGit(c), p.baseFor(c.Name)
	if exists, _ := g.BranchExists(ctx, branch); exists {
		if err := g.Checkout(ctx, branch, false); err != nil {
			return "", fmt.Errorf("checkout %s: %w", branch, err)
		}
		if fork == "" {
			cut, err := g.MergeBase(ctx, branch, base)
			if err != nil {
				p.logf("  ⚠ %s: fork point not pinned: %v", c.Name, err)
			}
			fork = cut
		}
	} else {
		if err := g.CreateBranch(ctx, branch, base); err != nil {
			return "", fmt.Errorf("branch %s off %s: %w", branch, base, err)
		}
		head, err := g.HeadSHA(ctx)
		if err != nil {
			p.logf("  ⚠ %s: fork point not pinned: %v", c.Name, err)
		}
		fork = head
	}
	status, err := g.WorktreeStatus(ctx)
	if err != nil {
		return "", fmt.Errorf("read tree: %w", err)
	}
	if status == "" {
		return fork, nil
	}
	if err := g.AddAll(ctx); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}
	if err := g.Commit(ctx, message, false); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	p.logf("  %s: committed on %s", c.Name, branch)
	return fork, nil
}

// crossLinkPRs rewrites every PR body once all of them exist so each one names its
// siblings. It has to be a second pass: a PR's URL is only known after gh created
// it, so the first one written cannot carry the last one's link. The rewrite is
// cosmetic — one gh refuses warns and leaves the run alone, because stranding a
// green cross-repo change over a body edit is the worse outcome.
func (p *Pipeline) crossLinkPRs(ctx context.Context, targets []shipTarget, body string) {
	if len(targets) < 2 {
		return
	}
	for _, t := range targets {
		if t.PRURL == "" {
			continue
		}
		if err := p.childGitHub(t.Child).UpdatePRBody(ctx, prNumber(t.PRURL), body+siblingSection(targets, t.Name)); err != nil {
			p.logf("  ⚠ could not cross-link the pull requests from %s: %v", t.Name, err)
		}
	}
}

// siblingSection lists the rest of a run's pull requests, so a reviewer landing on
// any one of them sees the whole cross-repo change.
func siblingSection(targets []shipTarget, name string) string {
	var b strings.Builder
	b.WriteString("\n## Ships with\n")
	for _, t := range targets {
		if t.Name == name || t.PRURL == "" {
			continue
		}
		b.WriteString("- " + t.Name + ": " + t.PRURL + "\n")
	}
	return b.String()
}

func (p *Pipeline) openChildPR(ctx context.Context, c folderrepo.Child, branch, title, body string) (string, error) {
	g, gh, base := p.childGit(c), p.childGitHub(c), p.baseFor(c.Name)
	if err := g.Push(ctx, p.Remote, branch, false); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}
	if url, _, err := gh.PRURL(ctx, branch); err == nil && url != "" {
		return url, nil
	}
	if err := p.assertPRBaseCurrent(ctx, g, base, base); err != nil {
		return "", err
	}
	url, err := gh.CreatePR(ctx, base, branch, title, body, false)
	if err != nil {
		return "", fmt.Errorf("pr create: %w", err)
	}
	return strings.TrimSpace(url), nil
}

// recordShipTargets stamps the plural ship set on the checkpoint: the children,
// the PR each one's branch carries and the commit each one's branch was cut at.
// They ride as their own keys rather than a schema change, and PR/PR_URL keep
// naming the first target so every surface built for one PR still reads.
func (p *Pipeline) recordShipTargets(id string, targets []shipTarget) error {
	names := make([]string, 0, len(targets))
	urls := make([]string, 0, len(targets))
	forks := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
		if t.PRURL != "" {
			urls = append(urls, t.Name+"="+t.PRURL)
		}
		if t.Fork != "" {
			forks = append(forks, t.Name+"="+t.Fork)
		}
	}
	if err := p.State.Set(id, "SHIP_TARGETS", strings.Join(names, ",")); err != nil {
		return fmt.Errorf("record ship targets: %w", err)
	}
	if len(forks) > 0 {
		if err := p.State.Set(id, "FORK_POINTS", strings.Join(forks, ",")); err != nil {
			return fmt.Errorf("record ship fork points: %w", err)
		}
	}
	if len(urls) == 0 {
		return nil
	}
	if err := p.State.Set(id, "PR_URLS", strings.Join(urls, ",")); err != nil {
		return fmt.Errorf("record ship PRs: %w", err)
	}
	return nil
}

// shipTargets recovers the run's ship set from its checkpoint.
func (p *Pipeline) shipTargets(id string) []shipTarget {
	urls := childValues(p.State.Get(id, "PR_URLS"))
	forks := childValues(p.State.Get(id, "FORK_POINTS"))
	targets := []shipTarget{}
	for _, name := range strings.Split(p.State.Get(id, "SHIP_TARGETS"), ",") {
		if name == "" {
			continue
		}
		targets = append(targets, shipTarget{
			Child: folderrepo.Child{Name: name, Path: filepath.Join(p.RepoRoot, name)},
			PRURL: urls[name],
			Fork:  forks[name],
		})
	}
	return targets
}

// childValues reads one of the ship keys' name=value lists back. The separator is
// a comma, not the census keys' "; ": neither a PR URL nor a commit sha carries one.
func childValues(recorded string) map[string]string {
	values := map[string]string{}
	for _, pair := range strings.Split(recorded, ",") {
		if name, value, ok := strings.Cut(pair, "="); ok {
			values[name] = value
		}
	}
	return values
}

// folderRunFootprint names every Child repo this run left work in. Once ship has
// recorded a set that set is the whole footprint: the run's work is committed on
// those branches, so anything still loose in any child by then is the operator's,
// not the run's. Before ship it is every child dirtied since the start-of-run
// census — the only reading of the run's work there is.
func (p *Pipeline) folderRunFootprint(ctx context.Context, id string) []folderrepo.Child {
	targets := p.shipTargets(id)
	if len(targets) == 0 {
		return p.changedChildren(ctx)
	}
	children := make([]folderrepo.Child, 0, len(targets))
	for _, t := range targets {
		children = append(children, t.Child)
	}
	return children
}

// folderCIAndMerge is the all-green gate: every changed Child repo's PR has to go
// green before any of them merges. A half-merged cross-repo change is the one
// outcome that breaks deployed services, so the first PR that is not green gives
// the ticket up with all of them still open, for the repair loop to work from.
func (p *Pipeline) folderCIAndMerge(ctx context.Context, id string) error {
	targets := p.shipTargets(id)
	if len(targets) == 0 {
		return p.giveUp(ctx, id, "no child repo recorded as shipped")
	}
	if p.localDelivery(ctx) {
		return p.landFolderLocally(ctx, id, targets)
	}
	p.setActivity(id, activity.CIWait, "")
	for _, t := range targets {
		if t.PRURL == "" {
			return p.giveUp(ctx, id, "no PR recorded for "+t.Name)
		}
		if err := p.pollCIWith(ctx, p.childGitHub(t.Child), t.Path, prNumber(t.PRURL), p.baseFor(t.Name)); err != nil {
			p.logf("  ✗ CI in %s: %v", t.Name, err)
			return p.giveUp(ctx, id, "CI not green in "+t.Name)
		}
	}
	if hold := p.mergeHold(id); hold.held() {
		p.handOverMerge(id, hold)
		p.logf("  ⏳ %s is green in every changed child repo — merge these yourself (%s): %s", id, hold.Reason, strings.Join(targetURLs(targets), ", "))
		return nil
	}
	p.setActivity(id, activity.Merge, "")
	for _, t := range targets {
		pr := prNumber(t.PRURL)
		if err := p.retryGH(ctx, "merge "+t.Name, func() error {
			return p.childGitHub(t.Child).Merge(ctx, pr, p.MergeMethod, true)
		}); err != nil {
			return fmt.Errorf("merge %s in %s: %w", id, t.Name, err)
		}
		p.logf("  ✓ merged %s", t.PRURL)
	}
	return p.markDone(ctx, id, "  ✓ merged %s in every changed child repo, marked Done")
}

// landFolderLocally is the remote-less fan-out: each changed Child repo's branch
// is squash-merged into its own base, and the ticket settles only once every one
// of them has landed.
func (p *Pipeline) landFolderLocally(ctx context.Context, id string, targets []shipTarget) error {
	branch := p.State.Get(id, "BRANCH")
	if hold := p.mergeHold(id); hold.held() {
		p.handOverMerge(id, hold)
		p.logf("  ⏳ %s is ready on %s in %s — merge them into each one's base yourself (%s)", id, branch, strings.Join(targetNames(targets), ", "), hold.Reason)
		return nil
	}
	p.setActivity(id, activity.Merge, "")
	message := deterministicCommitMessage(id, p.commitTitle(ctx, id))
	for _, t := range targets {
		g, base := p.childGit(t.Child), p.baseFor(t.Name)
		if err := g.Checkout(ctx, base, false); err != nil {
			return p.giveUp(ctx, id, fmt.Sprintf("could not check out %s in %s: %v", base, t.Name, err))
		}
		if err := g.SquashMerge(ctx, branch, message); err != nil {
			return p.giveUp(ctx, id, fmt.Sprintf("could not squash-merge %s into %s in %s: %v", branch, base, t.Name, err))
		}
		p.logf("  ✓ %s: %s squash-merged into %s (%s)", t.Name, branch, base, localDeliveryNote)
	}
	return p.markDone(ctx, id, "  ✓ delivered %s locally in every changed child repo, marked Done")
}

// folderRemoteExists answers the remote preflight for a Folder repo: the folder
// root has no git of its own, so its children decide whether this run delivers
// through a remote at all.
func (p *Pipeline) folderRemoteExists(ctx context.Context) bool {
	for _, c := range p.folderChildren() {
		if ok, err := p.childGit(c).RemoteExists(ctx, p.Remote); err == nil && ok {
			return true
		}
	}
	return false
}

func targetNames(targets []shipTarget) []string {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return names
}

func targetURLs(targets []shipTarget) []string {
	urls := make([]string, 0, len(targets))
	for _, t := range targets {
		urls = append(urls, t.PRURL)
	}
	return urls
}
