package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func (p *Pipeline) epicBranchName(ctx context.Context) (string, error) {
	if p.epicBranch != "" {
		return p.epicBranch, nil
	}

	// Resolve deterministically by epic ID, never by the drift-prone title slug. Any
	// existing epic/<ID>-* branch IS the epic branch and is adopted as-is — local
	// first, then the remote (a fresh clone or a different machine). The title slug
	// only names a brand-new branch on the very first creation. Matching on the slug
	// instead would let a renamed Linear epic spawn a SECOND branch that orphans the
	// children's integration work.
	if branch, _ := p.Git.FindEpicBranch(ctx, p.EpicID); branch != "" {
		p.epicBranch = branch
		return branch, nil
	}

	if !p.localDelivery(ctx) {
		remote, rerr := p.Git.FindRemoteEpicBranch(ctx, p.Remote, p.EpicID)
		if rerr != nil {
			// An indeterminate remote must NOT fall through to creating a duplicate.
			return "", fmt.Errorf("resolve epic branch for %s: check remote: %w", p.EpicID, rerr)
		}
		if remote != "" {
			if err := p.Git.CheckoutRemoteBranch(ctx, p.Remote, remote); err != nil {
				return "", fmt.Errorf("resolve epic branch %s: adopt from %s: %w", remote, p.Remote, err)
			}
			p.logf("  epic branch %s adopted from %s", remote, p.Remote)
			p.epicBranch = remote
			return remote, nil
		}
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only branch): %v", err)
	}
	branch := epicBranch(p.EpicID, title)
	base := p.epicBaseRef(ctx)
	if err := p.Git.CreateBranch(ctx, branch, base); err != nil {
		return "", &GiveUpError{ID: p.EpicID, Reason: "could not create epic branch for " + p.EpicID}
	}
	p.logf("  epic branch %s ← %s", branch, base)
	if !p.localDelivery(ctx) {
		if err := p.Git.Push(ctx, p.Remote, branch, false); err != nil {
			p.logf("  push epic branch error (continuing): %v", err)
		}
	}
	p.epicBranch = branch
	return branch, nil
}

func epicBranch(id, title string) string {
	if slug := slugify(title); slug != "" {
		return "epic/" + id + "-" + slug
	}
	return "epic/" + id
}

// epicBaseRef names the ref a brand-new epic branch is cut from: the base branch as
// the remote has it, fetched first. An epic is a long-lived integration branch every
// child stacks on, so cutting it from a local base that drifted behind the remote
// starts it behind those commits — each child PR then diffs against them as if they
// were the child's own work. Only a remote tip that cannot be resolved at all falls
// back to the local ref.
func (p *Pipeline) epicBaseRef(ctx context.Context) string {
	if p.localDelivery(ctx) {
		return p.baseRef()
	}
	tip := p.Remote + "/" + p.Base
	// A fetch exits 0 without creating the tracking ref when the clone's refspec
	// does not cover the base, so the tip is proven resolvable on both paths.
	fetched := p.fetchBaseTip(ctx)
	if known, _ := p.Git.ResolvesToCommit(ctx, tip); !known {
		p.logf("  ⚠ %s could not be resolved — cutting the epic from the local %s", tip, p.Base)
		return p.baseRef()
	}
	if !fetched {
		p.logf("  fetch %s failed — cutting the epic from the last known remote tip", tip)
	}
	return tip
}

func (p *Pipeline) ensureEpicPR(ctx context.Context, epicBranch string) (string, error) {
	prURL, _ := p.GitHub.PRURL(ctx, epicBranch)
	if prURL != "" {
		return prURL, nil
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		title = p.EpicID
	}
	prURL, err = p.GitHub.CreatePR(ctx, p.Base, epicBranch, epicPRTitle(p.EpicID, title), p.epicPRBody(p.EpicID))
	if err != nil {
		if strings.Contains(err.Error(), "No commits between") {
			if merged, _ := p.GitHub.MergedPRURL(ctx, epicBranch); merged != "" {
				p.logf("  epic PR already merged %s", merged)
				return merged, nil
			}
		}
		return "", err
	}
	p.logf("  epic PR %s", prURL)
	return prURL, nil
}

// epicPRTitle builds the epic PR's Conventional-Commit-style header —
// 'epic(<id>): <subject>' — so the squash of epic→main lands as a conventional
// subject. Tracker titles already carrying an "Epic:" prefix are stripped first
// so the header never stacks two markers, and the subject is case-conformed and
// truncated like a deterministic commit subject.
func epicPRTitle(id, title string) string {
	t := strings.TrimSpace(title)
	for {
		rest, ok := strings.CutPrefix(t, "Epic:")
		if !ok {
			rest, ok = strings.CutPrefix(t, "epic:")
		}
		if !ok {
			break
		}
		t = strings.TrimSpace(rest)
	}
	subject := strings.TrimRight(conformSubjectCase(commitSubject(t)), ".")
	if subject == "" {
		subject = id
	}
	return "epic(" + id + "): " + subject
}

// FinalizeEpic ships the epic only after every direct child is terminal. It is
// intentionally a loop-level finalizer, not part of a child merge: a child PR can
// land while siblings are still open, but the parent must not be shipped to main
// until the tracker confirms the whole child set is complete. Once it is, the epic
// branch is synced with the base (drift conflicts resolved by an agent), the epic
// PR is opened/adopted, its CI is gated with a bounded repair loop, and — when
// AUTO_MERGE is set — it is squash-merged to the base before the Linear epic closes.
// While any child still reads open it declines with an *EpicUnfinalizedError, so
// the caller can park the epic instead of mistaking the decline for a delivery.
func (p *Pipeline) FinalizeEpic(ctx context.Context) error {
	if p.EpicID == "" {
		return nil
	}
	statuser, ok := p.Tracker.(tracker.IssueStatuser)
	if !ok {
		p.logf("  epic close skipped — tracker cannot report child issue status")
		return nil
	}
	subs, err := p.Tracker.SubIssues(ctx, p.EpicID)
	if err != nil {
		return fmt.Errorf("finalize epic %s: list sub-issues: %w", p.EpicID, err)
	}
	if len(subs) == 0 {
		return nil
	}
	open, regressed, err := p.openSubIssues(ctx, statuser, subs)
	if err != nil {
		return err
	}
	for _, id := range regressed {
		p.reassertDone(ctx, id)
	}
	if len(open) > 0 {
		p.logf("  epic %s still open — waiting on %s", p.EpicID, strings.Join(open, ", "))
		return &EpicUnfinalizedError{EpicID: p.EpicID, Open: open}
	}

	epic, err := p.epicBranchName(ctx)
	if err != nil {
		return fmt.Errorf("finalize epic %s: resolve branch: %w", p.EpicID, err)
	}
	if p.localDelivery(ctx) {
		return p.finalizeEpicLocally(ctx, epic)
	}
	synced, err := p.syncEpicForMerge(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: sync with %s: %w", p.EpicID, p.Base, err)
	}
	prURL, err := p.ensureEpicPR(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: create PR: %w", p.EpicID, err)
	}
	if !synced {
		p.logf("  ⚠ epic %s still conflicts with %s — PR left for manual resolution: %s", p.EpicID, p.Base, prURL)
		return nil
	}

	merged, err := p.epicCIAndMerge(ctx, prURL)
	if err != nil {
		return fmt.Errorf("finalize epic %s: ship: %w", p.EpicID, err)
	}

	extra := "All direct sub-issues are delivered."
	if merged {
		extra += " Epic merged to " + p.Base + " via " + prURL + "."
	} else {
		extra += " Epic PR ready for review: " + prURL + "."
	}
	if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; PR %s", p.EpicID, prURL)
	if merged {
		p.reloadHubOntoBase(ctx)
	}
	return nil
}

// finalizeEpicLocally ships a complete epic on a remote-less repo: with no PR to
// open and no CI to gate, the epic branch is squash-merged into the base right here
// and the tracker epic closes on that. AUTO_MERGE=0 still means the operator lands
// the epic themselves, so it stays open on its branch until they do — there is no
// PR to poll, so this cannot block on the merge the way the remote path does. A
// merge git refuses leaves the epic branch untouched for a human, exactly as an
// unresolvable drift conflict does on the remote path.
func (p *Pipeline) finalizeEpicLocally(ctx context.Context, epic string) error {
	if !p.AutoMerge {
		p.logf("  ⏳ epic %s is complete on %s — merge it into %s yourself (AUTO_MERGE=0)", p.EpicID, epic, p.Base)
		return nil
	}
	if err := p.Git.Checkout(ctx, p.Base, false); err != nil {
		return fmt.Errorf("finalize epic %s: checkout %s: %w", p.EpicID, p.Base, err)
	}
	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only subject): %v", err)
	}
	if err := p.Git.SquashMerge(ctx, epic, epicPRTitle(p.EpicID, title)); err != nil {
		p.logf("  ⚠ epic %s could not be merged into %s (%v) — branch left for manual resolution", p.EpicID, p.Base, err)
		return nil
	}
	p.checkpointEpicMerged(ctx, "")
	_ = p.State.Set(p.EpicID, "DELIVERY", deliveryLocal)
	extra := "All direct sub-issues are delivered. Epic squash-merged into " + p.Base + " — " + localDeliveryNote + "."
	if err := p.Tracker.SetStatus(ctx, p.EpicID, tracker.StageDone, extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; merged into %s locally", p.EpicID, p.Base)
	p.reloadHubOntoBase(ctx)
	return nil
}

// syncEpicBest keeps the epic branch current between children: the local epic is
// first fast-forwarded from the REMOTE epic — siblings squash-merge into the
// remote, so a stale local epic would hand the next child a base missing that
// (squashed) work, tempting its build agent to merge the sibling's raw feature
// branch and poisoning the child's PR with commits the epic only ever contains in
// squashed form (a guaranteed merge conflict). Then a clean merge of the remote
// base is pushed so the next child branches off an up-to-date epic. A conflicting
// merge is aborted and deferred to the authoritative finalize sync (which runs a
// resolving agent). Best-effort by design — any failure is logged, never blocking
// the child about to branch off.
func (p *Pipeline) syncEpicBest(ctx context.Context, epic string) {
	if p.localDelivery(ctx) {
		return
	}
	if err := p.Git.Checkout(ctx, epic, false); err != nil {
		p.logf("  epic sync skipped (checkout %s: %v)", epic, err)
		return
	}
	if err := p.Git.Pull(ctx, p.Remote, epic); err != nil {
		p.logf("  epic pull from %s skipped (%v)", p.Remote, err)
	}
	conflicted, err := p.Git.MergeRemote(ctx, p.Remote, p.Base)
	switch {
	case err != nil:
		p.logf("  epic sync skipped (merge %s: %v)", p.Base, err)
	case conflicted:
		_ = p.Git.MergeAbort(ctx)
		p.logf("  epic %s conflicts with %s — deferring resolution to epic finalize", epic, p.Base)
	default:
		if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
			p.logf("  push synced epic branch error (continuing): %v", err)
		}
	}
}

// assertEpicBaseCurrent makes the remote epic branch carry the commit the run was
// cut from, before a child PR is opened against it. syncEpicBest pushes the synced
// epic best-effort, which is right mid-run but not here: a push that never landed
// leaves the remote epic behind the recorded fork point, and GitHub then diffs the
// child against a base older than the branch point — burying the base's own drift in
// the child's PR as thousands of foreign lines. A remote a push can repair is
// repaired; one that still misses the fork point fails the phase.
func (p *Pipeline) assertEpicBaseCurrent(ctx context.Context, id, epic string) error {
	pin := p.State.Get(id, "BASE_SHA")
	if pin == "" {
		return nil
	}
	carries, err := p.remoteCarries(ctx, epic, pin)
	if err != nil {
		return err
	}
	if carries {
		return nil
	}
	p.logf("  ⚠ %s/%s is behind the commit %s was cut from — pushing the epic branch before opening the PR", p.Remote, epic, id)
	if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
		return fmt.Errorf("push epic base %s: %w", epic, err)
	}
	carries, err = p.remoteCarries(ctx, epic, pin)
	if err != nil {
		return err
	}
	if !carries {
		return fmt.Errorf("epic base %s/%s does not carry %s, the commit %s was cut from — a PR opened against it would diff against a stale base", p.Remote, epic, pin, id)
	}
	p.logf("  ↻ %s/%s repaired — the PR base now carries %s", p.Remote, epic, pin)
	return nil
}

// remoteCarries reports whether the remote copy of branch contains commit. The
// remote's own tip is read with ls-remote, never a remote-tracking ref: a push that
// failed leaves the tracking ref exactly as convincing as a push that worked. A tip
// that moved on — a sibling squash-merged into the epic since — is fetched so its
// reachability can be judged locally.
func (p *Pipeline) remoteCarries(ctx context.Context, branch, commit string) (bool, error) {
	sha, err := p.Git.RemoteSHA(ctx, p.Remote, branch)
	if err != nil {
		return false, fmt.Errorf("read %s/%s: %w", p.Remote, branch, err)
	}
	if sha == "" {
		return false, nil
	}
	if known, _ := p.Git.ResolvesToCommit(ctx, sha); !known {
		if err := p.Git.Fetch(ctx, p.Remote, branch); err != nil {
			return false, fmt.Errorf("fetch %s/%s: %w", p.Remote, branch, err)
		}
	}
	return p.Git.IsAncestor(ctx, commit, sha)
}

// syncEpicForMerge brings the base into the epic branch before the epic ships to
// main so the epic PR is mergeable. The local epic is first fast-forwarded from
// the remote epic (children squash-merged into the remote; pushing a stale local
// epic would be rejected as non-fast-forward). A clean merge is pushed; a drift
// conflict is resolved by a bounded repair-agent loop, then the merge is completed
// and pushed. Returns false (with the merge aborted) when the conflicts could not
// be resolved, so the caller leaves the PR open for a human instead of shipping a
// broken merge.
func (p *Pipeline) syncEpicForMerge(ctx context.Context, epic string) (bool, error) {
	if err := p.Git.Checkout(ctx, epic, false); err != nil {
		return false, fmt.Errorf("checkout %s: %w", epic, err)
	}
	if err := p.Git.Pull(ctx, p.Remote, epic); err != nil {
		p.logf("  epic pull from %s skipped (%v)", p.Remote, err)
	}
	return p.syncBranchWithBase(ctx, p.EpicID, epic, p.Base, "epic-sync")
}

// epicCIAndMerge gates the epic PR on CI and ships it to the base: with AUTO_MERGE
// set it squash-merges once green; without it, it waits for the operator to merge the
// green PR by hand (a close without merge is a rejection → give-up, leaving the epic
// branch intact and unshipped). A red gate drives a bounded repair-agent loop on the
// epic branch before re-polling; an unrecoverable gate leaves the PR open for review.
// The bool reports whether the epic actually shipped to the base, so the caller closes
// the Linear epic with the right comment.
func (p *Pipeline) epicCIAndMerge(ctx context.Context, prURL string) (bool, error) {
	pr := prNumber(prURL)
	if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
		p.checkpointEpicMerged(ctx, prURL)
		return true, nil
	}

	for repair := 0; ; {
		if err := p.pollCI(ctx, pr); err == nil {
			break
		} else {
			p.logf("  ✗ epic CI: %v", err)
		}
		if repair >= p.MaxRepairs {
			p.logf("  ⚠ epic CI not green after %d repair attempt(s) — leaving PR for review: %s", repair, prURL)
			return false, nil
		}
		repair++
		epic, err := p.epicBranchName(ctx)
		if err != nil {
			return false, err
		}
		if err := p.Git.Checkout(ctx, epic, false); err != nil {
			return false, fmt.Errorf("epic repair %d: checkout %s: %w", repair, epic, err)
		}
		p.logf("  ⚠ epic CI red — repair attempt %d/%d", repair, p.MaxRepairs)
		if _, err := p.agentStep(ctx, p.EpicID, fmt.Sprintf("epic-repair%d", repair), epicRepairInstruction(p.prompts, p.EpicID, prURL, epic)); err != nil {
			return false, err
		}
		if err := p.Git.Push(ctx, p.Remote, epic, false); err != nil {
			p.logf("  push epic repair error (continuing): %v", err)
		}
	}

	if !p.AutoMerge {
		merged, err := p.waitForManualMerge(ctx, p.EpicID, pr, prURL)
		if err != nil {
			return false, err
		}
		if !merged {
			p.setPRStatus(p.EpicID, prStatusClosed)
			return false, p.giveUp(ctx, p.EpicID, fmt.Sprintf("epic PR #%s closed without merge", pr))
		}
		p.checkpointEpicMerged(ctx, prURL)
		p.logf("  ✓ epic merged to %s via %s", p.Base, prURL)
		return true, nil
	}
	if err := p.retryGH(ctx, "gh pr merge", func() error {
		if st, _ := p.GitHub.PRState(ctx, pr); st == "MERGED" {
			return nil
		}
		return p.GitHub.Merge(ctx, pr, p.MergeMethod, true)
	}); err != nil {
		return false, fmt.Errorf("merge epic PR %s: %w", prURL, err)
	}
	p.checkpointEpicMerged(ctx, prURL)
	p.logf("  ✓ epic merged to %s via %s", p.Base, prURL)
	return true, nil
}

// checkpointEpicMerged records the shipped epic as a merged run — title, PR and
// terminal phase beside its PR status. The epic id carries no checkpoint of its
// own until it ships, so stamping PR_STATUS alone would leave the board a
// phase-less row it reads as a run still in flight. The phase stays terminal on
// purpose: an in-flight one would make the epic id a resume target.
func (p *Pipeline) checkpointEpicMerged(ctx context.Context, prURL string) {
	if err := p.State.Set(p.EpicID, "PHASE", state.Merged); err != nil {
		p.logf("  epic checkpoint merged error (continuing): %v", err)
		return
	}
	if title, _ := p.Tracker.Title(ctx, p.EpicID); title != "" {
		_ = p.State.Set(p.EpicID, "TITLE", title)
	}
	if prURL != "" {
		_ = p.State.Set(p.EpicID, "PR", prNumber(prURL))
		_ = p.State.Set(p.EpicID, "PR_URL", prURL)
	}
	p.setPRStatus(p.EpicID, prStatusMerged)
}

func resolveConflictsInstruction(r prompts.Renderer, id, base, branch string) string {
	return r.Render("resolve_conflicts", prompts.ResolveConflictsData{ID: id, Base: base, Branch: branch})
}

func epicRepairInstruction(r prompts.Renderer, epicID, prURL, branch string) string {
	return r.Render("epic_repair", prompts.EpicRepairData{EpicID: epicID, PRURL: prURL, Branch: branch})
}

// openSubIssues splits the children into the ones that still block the epic and
// the ones trau delivered whose tracker status regressed afterwards. Beyond the
// tracker's own verdict, only trau's full delivery record outranks it — a merged
// checkpoint AND the TRACKER_DONE marker written once the tracker confirmed the
// close, which is exactly what an external automation can undo behind trau's back.
// A merged checkpoint alone means trau never saw the ticket close, and a status it
// could not read at all says nothing, so both keep blocking.
func (p *Pipeline) openSubIssues(ctx context.Context, statuser tracker.IssueStatuser, subs []tracker.SubIssue) ([]string, []string, error) {
	var open, regressed []string
	for _, sub := range subs {
		st, err := statuser.IssueStatus(ctx, sub.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("finalize epic %s: status %s: %w", p.EpicID, sub.ID, err)
		}
		if st.Terminal() {
			continue
		}
		switch {
		case st == tracker.StatusUnknown:
			open = append(open, sub.ID+" (unknown)")
		case p.deliveredByTrau(sub.ID):
			p.logf("  %s is not closed in the tracker but trau merged it — counting it delivered", sub.ID)
			regressed = append(regressed, sub.ID)
		default:
			open = append(open, sub.ID)
		}
	}
	return open, regressed, nil
}

// deliveredByTrau reports whether trau's own record proves it shipped id: the
// checkpoint reached the merged phase and the tracker confirmed the Done write.
func (p *Pipeline) deliveredByTrau(id string) bool {
	return p.State.Get(id, "PHASE") == state.Merged && p.State.Get(id, "TRACKER_DONE") == "1"
}

// reassertDone re-closes a delivered child whose tracker status regressed after
// trau itself marked it Done. The merged checkpoint already settles terminality,
// so the write is best-effort and never blocks the finalize.
func (p *Pipeline) reassertDone(ctx context.Context, id string) {
	note := "Delivered by trau"
	if pr := p.State.Get(id, "PR"); pr != "" {
		note += " in PR #" + pr
	}
	note += " and moved out of Done afterwards — restoring it."
	if err := p.Tracker.SetStatus(ctx, id, tracker.StageDone, note); err != nil {
		p.logf("  re-assert Done for %s error (continuing): %v", id, err)
		return
	}
	p.logf("  ↻ %s re-closed after a tracker status regression", id)
}
