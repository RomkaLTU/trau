package tracker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/tracker/azureapi"
)

// AzureDevOps drives tickets on Azure DevOps Services through the Work Item
// Tracking REST API. Unlike the Linear, Jira and GitHub providers it has no
// agent/MCP path: every operation resolves over HTTP as the single identity
// behind the configured personal access token, so a missing or rejected token is
// surfaced rather than quietly answered by some other account.
//
// Azure DevOps numbers work items organization-wide and gives them no per-project
// key, while the rest of trau addresses tickets as <PREFIX>-<n>. The provider
// therefore renders every identifier through the repo's configured issue prefix —
// work item 1234 in a TRAU-prefixed repo is TRAU-1234 — and parses the trailing
// number back out before calling the API. This is the same prefix mapping the
// GitHub provider applies to issue numbers.
type AzureDevOps struct {
	OrgURL          string // organization URL, e.g. https://dev.azure.com/acme
	PAT             string // personal access token (Basic-auth password)
	Project         string // Azure DevOps team project name
	AreaPath        string // Area Path the pick is confined to; empty is the whole project
	ReadyLabel      string
	QuarantineLabel string
	SplitLabel      string
	StatusOverrides map[Stage]string
}

func (a *AzureDevOps) api() *azureapi.Client {
	return azureapi.New(a.OrgURL, a.PAT)
}

// projectFor returns the team project to address: the configured project,
// falling back to the scope's team when the field is unset.
func (a *AzureDevOps) projectFor(scope Scope) string {
	if p := strings.TrimSpace(a.Project); p != "" {
		return p
	}
	return strings.TrimSpace(scope.Team)
}

// workItemID recovers the Azure DevOps work-item number behind a trau identifier
// (TRAU-1234 → 1234). A bare number is accepted too, so an id typed the way Azure
// DevOps itself displays it still resolves.
func workItemID(id string) (int, error) {
	num := strings.TrimSpace(id)
	if i := strings.LastIndex(num, "-"); i >= 0 {
		num = num[i+1:]
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("azure: %q is not an Azure DevOps work-item identifier", id)
	}
	return n, nil
}

// azureIdentifier renders a work-item number as the prefixed identifier the loop, the
// branch names and the sentinel parsers all expect.
func azureIdentifier(prefix string, id int) string {
	return prefix + "-" + strconv.Itoa(id)
}

// azureParentIdentifier renders a parent work-item number, or "" for a top-level item
// (Azure DevOps reports no parent as the zero id).
func azureParentIdentifier(prefix string, id int) string {
	if id <= 0 {
		return ""
	}
	return azureIdentifier(prefix, id)
}

// Pick returns the next eligible ticket identifier, or "" when nothing is
// eligible. Candidates come back tag-filtered and ranked from the API; this
// applies the policy WIQL cannot express: unstarted, not a container, and no
// unresolved blocker. Epic scope narrows the same ranked set to the epic's
// confirmed leaves.
func (a *AzureDevOps) Pick(ctx context.Context, scope Scope) (string, error) {
	var leaves map[int]bool
	if scope.Parent != "" {
		found, err := a.leafChildren(ctx, scope)
		if err != nil {
			return "", fmt.Errorf("pick %s: list children: %w", scope.Parent, err)
		}
		if len(found) == 0 {
			return "", nil
		}
		leaves = found
	}

	candidates, err := a.api().Eligible(ctx, a.projectFor(scope), a.AreaPath, a.ReadyLabel)
	if err != nil {
		return "", err
	}
	for _, c := range candidates {
		if leaves != nil && !leaves[c.ID] {
			continue
		}
		if azureStartable(c) {
			return azureIdentifier(scope.prefix(), c.ID), nil
		}
	}
	return "", nil
}

// azureStartable reports whether a candidate is work the loop may begin: an unstarted
// leaf with every blocker resolved. A work item with children is a container
// (epic or feature), never a slice to build.
func azureStartable(c azureapi.Candidate) bool {
	return c.Category() == azureapi.CategoryProposed && !c.HasChildren() && c.BlockersResolved
}

// leafChildren returns the ids of the epic's children that are still runnable
// work — leaves the tracker does not already consider finished.
func (a *AzureDevOps) leafChildren(ctx context.Context, scope Scope) (map[int]bool, error) {
	parent, err := workItemID(scope.Parent)
	if err != nil {
		return nil, err
	}
	children, err := a.api().Children(ctx, a.projectFor(scope), parent)
	if err != nil {
		return nil, err
	}
	leaves := make(map[int]bool, len(children))
	for _, ch := range children {
		if !ch.HasChildren() && !ch.Done() {
			leaves[ch.ID] = true
		}
	}
	return leaves, nil
}

// ListEligible enumerates the tickets the loop could pick next. Unlike Pick it
// keeps containers in the list — the caller decides what to do with an epic.
func (a *AzureDevOps) ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	candidates, err := a.api().Eligible(ctx, a.projectFor(scope), a.AreaPath, a.ReadyLabel)
	if err != nil {
		return nil, err
	}
	prefix := scope.prefix()
	out := make([]ListedTicket, 0, len(candidates))
	for _, c := range candidates {
		if !c.BlockersResolved {
			continue
		}
		out = append(out, ListedTicket{
			ID:          azureIdentifier(prefix, c.ID),
			Title:       c.Title,
			State:       c.State,
			Labels:      c.Tags,
			Parent:      azureParentIdentifier(prefix, c.Parent),
			HasChildren: c.HasChildren(),
		})
	}
	return out, nil
}

// ListTeams enumerates the Azure DevOps team projects the token can see — the
// provider's analogue of a Linear team, and what the repo stores as its tracker
// key. Project names double as their own identifier, so key and name match.
func (a *AzureDevOps) ListTeams(ctx context.Context) ([]Team, error) {
	projects, err := a.api().ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Team, 0, len(projects))
	for _, p := range projects {
		out = append(out, Team{Key: p.Name, Name: p.Name})
	}
	return out, nil
}

// SubIssues returns the direct children of a work item, read through its
// hierarchy relations.
func (a *AzureDevOps) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	n, err := workItemID(id)
	if err != nil {
		return nil, err
	}
	children, err := a.api().Children(ctx, a.Project, n)
	if err != nil {
		return nil, err
	}
	prefix := prefixOf(id)
	out := make([]SubIssue, 0, len(children))
	for _, ch := range children {
		out = append(out, SubIssue{
			ID:          azureIdentifier(prefix, ch.ID),
			Title:       ch.Title,
			Done:        ch.Done(),
			HasChildren: ch.HasChildren(),
		})
	}
	return out, nil
}

// Title returns the System.Title of a work item.
func (a *AzureDevOps) Title(ctx context.Context, id string) (string, error) {
	item, err := a.workItem(ctx, id)
	if err != nil {
		return "", err
	}
	return item.Title, nil
}

// IssueStatus reports the normalized lifecycle bucket of a work item, used by
// epic finalization and stale-checkpoint reconcile.
func (a *AzureDevOps) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	item, err := a.workItem(ctx, id)
	if err != nil {
		return StatusUnknown, err
	}
	return mapAzureStatus(item.Category(), item.Reason), nil
}

// mapAzureStatus maps a work item's state category onto the normalized status. A
// Resolved item is still live work under review, so it reports as started. An
// unrecognized category (a customized process) is unknown, so reconcile leaves
// the checkpoint intact rather than guessing.
func mapAzureStatus(category azureapi.StateCategory, reason string) IssueStatus {
	switch category {
	case azureapi.CategoryProposed:
		return StatusOpen
	case azureapi.CategoryInProgress, azureapi.CategoryResolved:
		return StatusStarted
	case azureapi.CategoryCompleted:
		if isCanceledReason(reason) {
			return StatusCanceled
		}
		return StatusDone
	case azureapi.CategoryRemoved:
		return StatusCanceled
	default:
		return StatusUnknown
	}
}

// isCanceledReason reports whether the System.Reason behind a closed work item
// means it was dropped rather than delivered. Azure DevOps closes a discarded
// item into the same Completed category as a finished one, so the reason is the
// only discriminator.
func isCanceledReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "cut", "removed", "obsolete", "duplicate", "deferred", "abandoned",
		"rejected", "won't fix", "wont fix", "as designed", "cannot reproduce":
		return true
	default:
		return false
	}
}

// IssueProject reports the team project a work item belongs to, used by the
// ownership guard to refuse cross-project runs.
func (a *AzureDevOps) IssueProject(ctx context.Context, id string) (string, error) {
	item, err := a.workItem(ctx, id)
	if err != nil {
		return "", err
	}
	return item.Project, nil
}

// ParentIssue reports the identifier of a work item's parent (the epic or feature
// it hangs off), or "" when it is top-level.
func (a *AzureDevOps) ParentIssue(ctx context.Context, id string) (string, error) {
	item, err := a.workItem(ctx, id)
	if err != nil {
		return "", err
	}
	return azureParentIdentifier(prefixOf(id), item.Parent), nil
}

// IssueDetail returns the title, body and discussion of a work item for
// build-prompt context. The body arrives as HTML and is rendered to markdown with
// the acceptance criteria appended.
func (a *AzureDevOps) IssueDetail(ctx context.Context, id string) (IssueDetail, error) {
	n, err := workItemID(id)
	if err != nil {
		return IssueDetail{}, err
	}
	item, err := a.api().WorkItem(ctx, a.Project, n)
	if err != nil {
		return IssueDetail{}, err
	}
	detail := IssueDetail{Title: item.Title, Description: item.Description, Labels: item.Tags}
	// The discussion is an enrichment on top of an enrichment: losing it must not
	// cost the prompt the description that already loaded.
	comments, err := a.api().Comments(ctx, a.Project, n)
	if err != nil {
		logger.Debugf("azure: read comments for %s: %v", id, err)
		return detail, nil
	}
	for _, c := range comments {
		detail.Comments = append(detail.Comments, IssueComment{Author: c.Author, Body: c.Body})
	}
	return detail, nil
}

// SetStatus moves a work item to the state its process template uses for stage,
// attaching extra as a discussion comment when supplied. Azure DevOps writes
// System.State directly, so the work is matching the stage against the states the
// project's template actually declares.
func (a *AzureDevOps) SetStatus(ctx context.Context, id string, stage Stage, extra string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	state, err := a.resolveState(ctx, n, stage)
	if err != nil {
		return err
	}
	return a.api().SetState(ctx, a.Project, n, state, extra)
}

// resolveState picks the state name to write for stage. Each stock template names
// the same workflow stages differently — Agile calls started work Active, Scrum
// Committed, Basic Doing — so a name match is only the first attempt; the state
// category the template reports carries the rest.
func (a *AzureDevOps) resolveState(ctx context.Context, id int, stage Stage) (string, error) {
	item, err := a.api().WorkItem(ctx, a.Project, id)
	if err != nil {
		return "", err
	}
	states, err := a.api().States(ctx, a.Project, item.Type)
	if err != nil {
		return "", err
	}
	options := make([]WorkflowOption, len(states))
	for i, s := range states {
		options[i] = WorkflowOption{Name: s.Name, Category: s.Category}
	}
	i, ok := ResolveStage(stage, a.StatusOverrides[stage], azureCategories(stage), options)
	if !ok {
		return "", fmt.Errorf("%w for %s on %s #%d (available: %s) — pin one with %s",
			azureapi.ErrNoState, stage.Display(), item.Type, id, optionNames(options), stage.ConfigKey())
	}
	return options[i].Name, nil
}

// azureCategories are the state categories a stage settles for when no state name
// matched. Scrum declares no Resolved category, so a review stage lands on the
// in-progress state instead of failing — the work is still live either way.
func azureCategories(stage Stage) []string {
	switch stage {
	case StageTodo:
		return []string{string(azureapi.CategoryProposed)}
	case StageInProgress:
		return []string{string(azureapi.CategoryInProgress), string(azureapi.CategoryResolved)}
	case StageInReview:
		return []string{string(azureapi.CategoryResolved), string(azureapi.CategoryInProgress)}
	case StageDone:
		return []string{string(azureapi.CategoryCompleted)}
	default:
		return nil
	}
}

// AddLabel adds one tag to a work item without disturbing its other tags.
func (a *AzureDevOps) AddLabel(ctx context.Context, id, label string) error {
	if label = strings.TrimSpace(label); label == "" {
		return nil
	}
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	return a.api().UpdateTags(ctx, a.Project, n, []string{label}, nil)
}

// RemoveLabel drops one tag from a work item without disturbing its other tags.
func (a *AzureDevOps) RemoveLabel(ctx context.Context, id, label string) error {
	if label = strings.TrimSpace(label); label == "" {
		return nil
	}
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	return a.api().UpdateTags(ctx, a.Project, n, nil, []string{label})
}

// Reset returns a ticket to a ready/unstarted state so the picker re-selects it:
// it drops the quarantine tag, ensures the ready tag, moves the work item back to
// an unstarted state and comments.
func (a *AzureDevOps) Reset(ctx context.Context, id string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	if err := a.api().UpdateTags(ctx, a.Project, n, []string{a.ReadyLabel}, []string{a.QuarantineLabel}); err != nil {
		return err
	}
	state, err := a.resolveState(ctx, n, StageTodo)
	if err != nil {
		return err
	}
	return a.api().SetState(ctx, a.Project, n, state, fmt.Sprintf("Trau loop reset %s to start fresh.", id))
}

// Quarantine marks a ticket unrecoverable: it drops the ready tag, adds the
// quarantine tag and comments with the reason.
func (a *AzureDevOps) Quarantine(ctx context.Context, id, reason string) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	if err := a.api().UpdateTags(ctx, a.Project, n, []string{a.QuarantineLabel}, []string{a.ReadyLabel}); err != nil {
		return err
	}
	return a.api().AddComment(ctx, a.Project, n,
		fmt.Sprintf("Trau loop stopped: %s (see this ticket's run in the trau web UI).", reason))
}

// FileBug files a NEW Bug work item as a last-resort HITL blocker for a QA
// failure the slice could not self-heal, even after comprehensive bugfix passes.
func (a *AzureDevOps) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	summary, description := bugContent(id, verdictPath)
	n, err := a.api().CreateWorkItem(ctx, a.Project, "Bug", summary, description, []string{"HITL"})
	if err != nil {
		return "", err
	}
	return azureIdentifier(prefixOf(id), n), nil
}

// EnsureLabels is a no-op on Azure DevOps: tags are freeform strings created
// implicitly the first time they are applied, so there is nothing to pre-create.
func (a *AzureDevOps) EnsureLabels(ctx context.Context) error {
	return nil
}

func (a *AzureDevOps) workItem(ctx context.Context, id string) (*azureapi.WorkItem, error) {
	n, err := workItemID(id)
	if err != nil {
		return nil, err
	}
	return a.api().WorkItem(ctx, a.Project, n)
}
