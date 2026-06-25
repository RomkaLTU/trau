package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func (p *Pipeline) epicBranchName(ctx context.Context) (string, error) {
	if p.epicBranch != "" {
		return p.epicBranch, nil
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		p.logf("  epic title lookup error (using id-only branch): %v", err)
	}
	branch := epicBranch(p.EpicID, title)

	exists, _ := p.Git.BranchExists(ctx, branch)
	if !exists {
		if err := p.Git.CreateBranch(ctx, branch, p.Base); err != nil {
			return "", &GiveUpError{ID: p.EpicID, Reason: "could not create epic branch for " + p.EpicID}
		}
		p.logf("  epic branch %s ← %s", branch, p.Base)
		if err := p.Git.Push(ctx, p.Remote, branch); err != nil {
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

func (p *Pipeline) ensureEpicPR(ctx context.Context, epicBranch string) (string, error) {
	prURL, _ := p.GitHub.PRURL(ctx, epicBranch)
	if prURL != "" {
		return prURL, nil
	}

	title, err := p.Tracker.Title(ctx, p.EpicID)
	if err != nil {
		title = p.EpicID
	}
	prURL, err = p.GitHub.CreatePR(ctx, p.Base, epicBranch, "Epic: "+title, epicPRBody(p.EpicID))
	if err != nil {
		return "", err
	}
	p.logf("  epic PR %s", prURL)
	return prURL, nil
}

func epicPRBody(id string) string {
	return fmt.Sprintf("## Summary\nEpic integration branch for %s.\n\nFeatures land on the epic branch first; this PR ships the epic to main once complete.\n\nLinear: %s", id, id)
}

// FinalizeEpic closes an epic only after every direct child is terminal, then
// opens or adopts the epic-branch PR to the base branch. It is intentionally a
// loop-level finalizer, not part of a child merge: a child PR can land while
// siblings are still open, but the parent must not be closed or shipped to main
// until the tracker confirms the whole child set is complete.
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
	open, err := p.openSubIssues(ctx, statuser, subs)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		p.logf("  epic %s still open — waiting on %s", p.EpicID, strings.Join(open, ", "))
		return nil
	}

	epic, err := p.epicBranchName(ctx)
	if err != nil {
		return fmt.Errorf("finalize epic %s: resolve branch: %w", p.EpicID, err)
	}
	prURL, err := p.ensureEpicPR(ctx, epic)
	if err != nil {
		return fmt.Errorf("finalize epic %s: create PR: %w", p.EpicID, err)
	}
	extra := "All direct sub-issues are closed."
	if prURL != "" {
		extra += " Epic PR: " + prURL + "."
	}
	if err := p.Tracker.SetStatus(ctx, p.EpicID, "Done", extra); err != nil {
		return fmt.Errorf("finalize epic %s: close epic: %w", p.EpicID, err)
	}
	p.logf("  ✓ epic %s closed; PR %s", p.EpicID, prURL)
	return nil
}

func (p *Pipeline) openSubIssues(ctx context.Context, statuser tracker.IssueStatuser, subs []tracker.SubIssue) ([]string, error) {
	var open []string
	for _, sub := range subs {
		st, err := statuser.IssueStatus(ctx, sub.ID)
		if err != nil {
			return nil, fmt.Errorf("finalize epic %s: status %s: %w", p.EpicID, sub.ID, err)
		}
		if st.Terminal() {
			continue
		}
		if st == tracker.StatusUnknown {
			open = append(open, sub.ID+" (unknown)")
			continue
		}
		open = append(open, sub.ID)
	}
	return open, nil
}
