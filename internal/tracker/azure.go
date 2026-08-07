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
// Azure DevOps numbers work items organization-wide, so a number identifies one
// work item on its own and trau uses it verbatim: work item 6694 is 6694
// everywhere — on the board, on the CLI, and as feature/6694-slug (ADR 0024 §1).
// There is no prefix to map through.
type AzureDevOps struct {
	OrgURL          string   // organization URL, e.g. https://dev.azure.com/acme
	PAT             string   // personal access token (Basic-auth password)
	Project         string   // Azure DevOps team project name
	AreaPath        string   // Area Path the pick is confined to; empty is the whole project
	Teams           []string // team names whose board areas the pick is confined to
	ReadyLabel      string
	QuarantineLabel string
	SplitLabel      string
	StatusOverrides map[Stage]string
	// boardStates maps the team's Kanban columns onto trau's status groups, empty
	// when the repo sets no AZURE_BOARD_STATES and grouping stays category-derived.
	boardStates azureBoardStates
}

func (a *AzureDevOps) api() *azureapi.Client {
	return azureapi.New(a.OrgURL, a.PAT)
}

// scope resolves the slice of the board the loop may pick from, which must be the
// slice the hub mirrors: a ticket the loop starts but the hub never synced has no
// row to confirm at the queue-by-id path (ADR 0028 §3).
func (a *AzureDevOps) scope(ctx context.Context, project string) (azureapi.BoardScope, error) {
	return a.api().ResolveScope(ctx, project, a.AreaPath, a.Teams)
}

// projectFor returns the team project to address: the configured project,
// falling back to the scope's team when the field is unset.
func (a *AzureDevOps) projectFor(scope Scope) string {
	if p := strings.TrimSpace(a.Project); p != "" {
		return p
	}
	return strings.TrimSpace(scope.Team)
}

// workItemID recovers the Azure DevOps work-item number behind an identifier. A
// prefixed form is still accepted (TRAU-1234 → 1234) so an id minted before the
// board moved to bare numbers, or one copied from a prefixed tracker, still
// resolves.
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

// azureIdentifier renders a work-item number as the identifier the loop, the branch
// names and the web UI all use — the number itself.
func azureIdentifier(id int) string {
	return strconv.Itoa(id)
}

// azureParentIdentifier renders a parent work-item number, or "" for a top-level item
// (Azure DevOps reports no parent as the zero id).
func azureParentIdentifier(id int) string {
	if id <= 0 {
		return ""
	}
	return azureIdentifier(id)
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

	project := a.projectFor(scope)
	board, err := a.scope(ctx, project)
	if err != nil {
		return "", err
	}
	candidates, err := a.api().Eligible(ctx, project, board, a.ReadyLabel)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	levels := a.levels(ctx, project)
	for _, c := range candidates {
		if leaves != nil && !leaves[c.ID] {
			continue
		}
		if azureStartable(c, levels.Of(c.Type)) {
			return azureIdentifier(c.ID), nil
		}
	}
	return "", nil
}

// levels reads the backlog level every work-item type in the project sits on.
func (a *AzureDevOps) levels(ctx context.Context, project string) azureapi.Levels {
	return readBacklogLevels(ctx, a.api(), project, a.Teams)
}

// readBacklogLevels reads the level model the project stacks its work-item types
// on, team-scoped because where a Bug lands is a team setting. The model is an
// enrichment on every path that reads it, so a configuration the PAT or the named
// team cannot see costs the level and nothing else: the pick, the pull and the
// prompt all carry on without one, as they did before trau read levels at all. A
// create is the exception — it cannot file a type it cannot place, so the writer
// keeps its own failing read.
func readBacklogLevels(ctx context.Context, client *azureapi.Client, project string, teams []string) azureapi.Levels {
	team := levelTeam(teams)
	if len(teams) > 1 {
		logger.Debugf("azure: repo names %d teams; backlog levels read from %q", len(teams), team)
	}
	levels, err := client.BacklogLevels(ctx, project, team)
	if err != nil {
		logger.Debugf("azure: read backlog levels for %s: %v", project, err)
	}
	return levels
}

// levelTeam names the team whose backlog configuration decides the board's level
// model: the first team the repo lists, or "" for the project's default team. A
// repo mirroring two boards that disagree about where a Bug sits is not a case
// worth modelling, so the first name wins.
func levelTeam(teams []string) string {
	if len(teams) == 0 {
		return ""
	}
	return strings.TrimSpace(teams[0])
}

// azureStartable reports whether a candidate is work the loop may begin: an
// unstarted leaf at requirement level or below, with every blocker resolved. A
// work item with children is a container, and so is anything the project's own
// backlog configuration places above requirement — a childless Feature carrying
// the ready tag is work nobody has broken down yet, not a slice to build.
func azureStartable(c azureapi.Candidate, level azureapi.Level) bool {
	if level == azureapi.LevelEpic || level == azureapi.LevelFeature {
		return false
	}
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
	project := a.projectFor(scope)
	board, err := a.scope(ctx, project)
	if err != nil {
		return nil, err
	}
	candidates, err := a.api().Eligible(ctx, project, board, a.ReadyLabel)
	if err != nil {
		return nil, err
	}
	out := make([]ListedTicket, 0, len(candidates))
	for _, c := range candidates {
		if !c.BlockersResolved {
			continue
		}
		out = append(out, ListedTicket{
			ID:          azureIdentifier(c.ID),
			Title:       c.Title,
			State:       c.State,
			Labels:      c.Tags,
			Parent:      azureParentIdentifier(c.Parent),
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
	out := make([]SubIssue, 0, len(children))
	for _, ch := range children {
		out = append(out, SubIssue{
			ID:          azureIdentifier(ch.ID),
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
// epic finalization and stale-checkpoint reconcile. A repo whose board columns are
// mapped reports what the board shows, so reconcile and the board never disagree
// about the same ticket (ADR 0036).
func (a *AzureDevOps) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	item, err := a.workItem(ctx, id)
	if err != nil {
		return StatusUnknown, err
	}
	if group, ok := a.boardStates.group(item.BoardColumn, item.State, item.Reason); ok {
		return issueStatusOf(group), nil
	}
	return mapAzureStatus(a.stateCategory(ctx, item), item.Reason), nil
}

// stateCategory classifies a work item's state against the categories its own
// work-item type declares, so a state a customized process renamed reports a
// lifecycle bucket instead of unknown. Metadata the token cannot read leaves the
// name table in charge rather than failing the read.
func (a *AzureDevOps) stateCategory(ctx context.Context, item *azureapi.WorkItem) azureapi.StateCategory {
	states, err := a.api().StateCategories(ctx, a.Project, item.Type)
	if err != nil {
		logger.Debugf("azure: read %s states: %v", item.Type, err)
	}
	return azureapi.CategoryOf(states, item.State)
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
	return azureParentIdentifier(item.Parent), nil
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
	detail := IssueDetail{Title: item.Title, Description: item.Description, Labels: item.Tags, Type: item.Type}
	detail.Level = string(a.levels(ctx, a.Project).Of(item.Type))
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
	states, err := a.api().StateCategories(ctx, a.Project, item.Type)
	if err != nil {
		return "", err
	}
	if stage == StageTodo {
		if state, ok := azureUnstartedState(states, a.StatusOverrides[stage]); ok {
			return state, nil
		}
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

// azureTodoNames ranks the names an Azure DevOps process gives the column a
// groomed, ready-to-pick item waits in, most preferred first. A process that
// splits its Proposed category into a raw intake column and a groomed one names
// the groomed one from the head of this list, so New and its siblings win only
// where the process offers nothing better. It is deliberately Azure-local: the
// shared Stage vocabulary orders these the other way round, and reordering it
// would move Jira and Linear too.
var azureTodoNames = []string{"ready", "approved", "selected", "to do", "todo", "new", "proposed", "backlog", "triage"}

// azureUnstartedState picks the state that means groomed but not started: the
// state a todo write targets, and the one Proposed state the board groups as
// unstarted rather than backlog. Both directions read the same answer, so the
// column the loop resets a ticket into is the column the board shows it in. A
// pinned STATUS_TODO wins outright, and an abandonment never stands in.
func azureUnstartedState(states []azureapi.State, pin string) (string, bool) {
	if pinned := strings.TrimSpace(pin); pinned != "" {
		for _, s := range states {
			if normalizeStatus(s.Name) == normalizeStatus(pinned) {
				return s.Name, true
			}
		}
	}
	best, bestRank := "", 0
	for _, s := range states {
		if azureapi.CategoryOf(states, s.Name) != azureapi.CategoryProposed || abandons(s.Name) {
			continue
		}
		if rank := azureTodoRank(s.Name); best == "" || rank < bestRank {
			best, bestRank = s.Name, rank
		}
	}
	return best, best != ""
}

// azureTodoRank scores how strongly a state name reads as the ready-to-pick
// column, lowest first. A name the vocabulary does not know ranks last, so the
// workflow's own order decides between two states neither of them claims.
func azureTodoRank(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	for i, want := range azureTodoNames {
		if strings.Contains(lower, want) {
			return i
		}
	}
	return len(azureTodoNames)
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

// PostQANote leaves the run's QA report on the work item's discussion, with the
// verify screenshots uploaded to the work item and embedded in it. Discussion
// bodies are HTML, so the Markdown is rendered on the way in. The uploads are
// also listed under the item's Attachments, which survives a discussion renderer
// that declines to show the images inline.
func (a *AzureDevOps) PostQANote(ctx context.Context, id string, note QANote) error {
	n, err := workItemID(id)
	if err != nil {
		return err
	}
	files := a.uploadQAImages(ctx, id, note.Images)
	if err := a.api().AddCommentWithImages(ctx, a.Project, n, note.Body, files); err != nil {
		return err
	}
	if err := a.api().AttachFiles(ctx, a.Project, n, files); err != nil {
		logger.Printf("azure: %s QA note screenshots not listed as attachments: %v", id, err)
	}
	return nil
}

// uploadQAImages stores the note's screenshots in the attachment store and
// returns the ones that landed. An upload that fails costs that screenshot and
// nothing else: the report still posts without it.
func (a *AzureDevOps) uploadQAImages(ctx context.Context, id string, images []QAImage) []azureapi.Attachment {
	if len(images) == 0 {
		return nil
	}
	api := a.api()
	files := make([]azureapi.Attachment, 0, len(images))
	for _, img := range images {
		at, err := api.UploadAttachment(ctx, a.Project, img.Name, img.Bytes)
		if err != nil {
			logger.Printf("azure: %s QA note dropped screenshot %s: %v", id, img.Name, err)
			continue
		}
		files = append(files, azureapi.Attachment{URL: at, Caption: img.Caption})
	}
	return files
}

// FileBug files a NEW Bug work item as a last-resort HITL blocker for a QA
// failure the slice could not self-heal, even after comprehensive bugfix passes.
func (a *AzureDevOps) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	summary, description := bugContent(id, verdictPath)
	api := a.api()
	created, err := api.CreateWorkItem(ctx, a.Project, azureapi.NewWorkItem{
		Type:     "Bug",
		Title:    summary,
		Body:     azureapi.SplitBody(description, azureHasAcceptance(azureapi.LevelRequirement)),
		Tags:     []string{"HITL"},
		Assignee: azureSelfAssign(ctx, api),
	})
	if err != nil {
		return "", err
	}
	azureRankTop(ctx, api, a.Project, created)
	return azureIdentifier(created.ID), nil
}

// azureSelfAssign resolves the identity a create is assigned to: whoever the
// personal access token belongs to. It is the one assignment the provider can make
// — identity search still needs the Graph host and a scope trau does not request
// (ADR 0031 §9, amended by ADR 0036) — and losing it costs the work item its
// assignee, never the create.
func azureSelfAssign(ctx context.Context, client *azureapi.Client) string {
	owner, err := client.Owner(ctx)
	if err != nil {
		logger.Printf("azure: filing unassigned; could not resolve the token's owner: %v", err)
		return ""
	}
	return owner.Assignee()
}

// azureRankTop moves a filed work item to the top of the board column it landed
// in. The position is presentation: losing it costs the item its place in the
// column and nothing else, so a failure is reported rather than returned to a
// caller whose work item already exists.
func azureRankTop(ctx context.Context, client *azureapi.Client, project string, created azureapi.Created) {
	if err := client.RankTop(ctx, project, created.BoardColumn, created.ID); err != nil {
		logger.Printf("azure: work item %d filed without a top-of-column rank: %v", created.ID, err)
	}
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
