package azureapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Work-item link types the tracker reads. Azure DevOps models hierarchy and
// dependency as named relations rather than fields: a Hierarchy-Reverse link
// points at the parent, Hierarchy-Forward at each child, and Dependency-Reverse
// at each predecessor — the items that must finish before this one can start.
const (
	relParent      = "System.LinkTypes.Hierarchy-Reverse"
	relChild       = "System.LinkTypes.Hierarchy-Forward"
	relPredecessor = "System.LinkTypes.Dependency-Reverse"
)

// priorityUnset ranks a work item whose type carries no priority field behind
// every explicit 1–4.
const priorityUnset = 5

// WorkItem is the subset of an Azure DevOps work item the tracker consumes.
type WorkItem struct {
	ID    int
	Title string
	// Description is the rich-text body rendered from HTML to markdown, with the
	// acceptance criteria appended as its own section when the type carries them.
	Description string
	// State is the raw System.State value, whose vocabulary depends on the
	// project's process; Category buckets it process-agnostically.
	State string
	// Reason is System.Reason, the qualifier behind a state change ("Cut",
	// "Duplicate") that separates a canceled work item from a completed one.
	Reason  string
	Type    string
	Project string
	Tags    []string
	// Priority is Microsoft.VSTS.Common.Priority (1 highest … 4 lowest), or 5 when
	// the work-item type carries no priority field.
	Priority int
	// Parent is the parent work item's id, 0 when top-level.
	Parent    int
	Children  []int
	BlockedBy []int
}

// HasChildren reports whether the work item is a parent — an epic or feature the
// loop must not run as a leaf.
func (w WorkItem) HasChildren() bool { return len(w.Children) > 0 }

// Category buckets the work item's state process-agnostically.
func (w WorkItem) Category() StateCategory { return Category(w.State) }

// Done reports whether the tracker already considers the work item finished,
// whether it completed or was removed.
func (w WorkItem) Done() bool { return w.Category().Terminal() }

// Candidate is one work item the loop could pick, with its blockers' resolution
// already looked up — an extra read Azure DevOps forces because a relation
// carries only the linked item's URL, never its state.
type Candidate struct {
	WorkItem
	BlockersResolved bool
}

// WorkItem fetches one work item by numeric id, including the relations the
// tracker derives parent, children and blockers from.
func (c *Client) WorkItem(ctx context.Context, project string, id int) (*WorkItem, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	if id <= 0 {
		return nil, ErrNotFound
	}
	var dst workItemResponse
	path := projectPath(project, "/workitems/"+strconv.Itoa(id)+"?$expand=relations")
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	item := dst.toWorkItem()
	return &item, nil
}

// WorkItems reads many work items in one round-trip per batchLimit-sized chunk,
// preserving the order of ids. Ids the credentials cannot see are simply absent
// from the result rather than an error.
func (c *Client) WorkItems(ctx context.Context, project string, ids []int) ([]WorkItem, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	out := make([]WorkItem, 0, len(ids))
	for start := 0; start < len(ids); start += batchLimit {
		chunk := ids[start:min(start+batchLimit, len(ids))]
		var dst struct {
			Value []workItemResponse `json:"value"`
		}
		path := projectPath(project, "/workitems?ids="+joinInts(chunk)+"&$expand=relations&errorPolicy=omit")
		if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
			return nil, err
		}
		for _, raw := range dst.Value {
			out = append(out, raw.toWorkItem())
		}
	}
	return out, nil
}

// Eligible returns the work items the loop could pick next: everything in
// project tagged with readyLabel, ranked by priority then id, each carrying
// whether all of its blockers are resolved. The tag filter runs server-side in
// WIQL; the remaining policy — unstarted, not a parent, blockers clear — is the
// caller's, matching what the other providers do with their own query languages.
func (c *Client) Eligible(ctx context.Context, project, readyLabel string) ([]Candidate, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	if strings.TrimSpace(readyLabel) == "" {
		return nil, nil
	}
	ids, err := c.query(ctx, project, eligibleWIQL(project, readyLabel))
	if err != nil {
		return nil, err
	}
	items, err := c.WorkItems(ctx, project, ids)
	if err != nil {
		return nil, err
	}
	rank(items)

	resolved, err := c.blockerStates(ctx, project, items)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(items))
	for _, item := range items {
		out = append(out, Candidate{WorkItem: item, BlockersResolved: allResolved(item.BlockedBy, resolved)})
	}
	return out, nil
}

// Children returns the direct children of a work item, read through its
// Hierarchy-Forward relations.
func (c *Client) Children(ctx context.Context, project string, id int) ([]WorkItem, error) {
	parent, err := c.WorkItem(ctx, project, id)
	if err != nil {
		return nil, err
	}
	if len(parent.Children) == 0 {
		return nil, nil
	}
	return c.WorkItems(ctx, project, parent.Children)
}

// blockerStates resolves the terminal-ness of every blocker referenced by items
// in one batch read, keyed by work-item id. Blockers the credentials cannot see
// are absent, which allResolved treats as unresolved.
func (c *Client) blockerStates(ctx context.Context, project string, items []WorkItem) (map[int]bool, error) {
	var ids []int
	seen := map[int]bool{}
	for _, item := range items {
		for _, id := range item.BlockedBy {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	blockers, err := c.WorkItems(ctx, project, ids)
	if err != nil {
		return nil, err
	}
	states := make(map[int]bool, len(blockers))
	for _, b := range blockers {
		states[b.ID] = b.Done()
	}
	return states, nil
}

// allResolved reports whether every blocker of a candidate sits in a terminal
// state. An unknown blocker counts as unresolved, so an unreadable dependency
// holds the ticket back rather than letting the loop start blocked work.
func allResolved(blockedBy []int, states map[int]bool) bool {
	for _, id := range blockedBy {
		if !states[id] {
			return false
		}
	}
	return true
}

// rank orders candidates the way the loop wants to start them: highest priority
// first, lowest id as the tie-breaker. Azure DevOps cannot order on a field a
// work-item type may not define, so the ranking happens here rather than in the
// ORDER BY clause.
func rank(items []WorkItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
}

// eligibleWIQL renders the flat WIQL query behind Eligible. Only the tag filter
// is expressed here: state vocabularies differ per process template, so the
// unstarted test is applied client-side against the normalized category instead
// of being hard-coded into a state-name list this query cannot know.
func eligibleWIQL(project, readyLabel string) string {
	return "SELECT [System.Id] FROM WorkItems" +
		" WHERE [System.TeamProject] = " + wiqlString(project) +
		" AND [System.Tags] CONTAINS " + wiqlString(readyLabel) +
		" ORDER BY [System.Id] ASC"
}

// wiqlString renders s as a WIQL string literal, doubling embedded quotes.
func wiqlString(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}

// query runs a flat WIQL query and returns the matching work-item ids. A flat
// query answers with ids only, so callers follow up with a batch read.
func (c *Client) query(ctx context.Context, project, wiql string) ([]int, error) {
	body, err := json.Marshal(map[string]string{"query": wiql})
	if err != nil {
		return nil, err
	}
	var dst struct {
		WorkItems []struct {
			ID int `json:"id"`
		} `json:"workItems"`
	}
	path := projectPath(project, "/wiql?$top="+strconv.Itoa(batchLimit))
	if err := c.do(ctx, http.MethodPost, path, body, &dst); err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(dst.WorkItems))
	for _, w := range dst.WorkItems {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// State is one state a work-item type can hold, with the process-agnostic
// category Azure DevOps reports alongside its process-specific name.
type State struct {
	Name     string
	Category string
}

// States lists the states a work-item type can hold, in workflow order. It backs
// the state resolution SetState performs: a target named for one process
// ("In Progress") has to be matched against whatever the project's own template
// calls that stage.
func (c *Client) States(ctx context.Context, project, itemType string) ([]State, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Value []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"value"`
	}
	path := projectPath(project, "/workitemtypes/"+url.PathEscape(strings.TrimSpace(itemType))+"/states")
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	out := make([]State, 0, len(dst.Value))
	for _, s := range dst.Value {
		out = append(out, State{Name: s.Name, Category: s.Category})
	}
	return out, nil
}

type workItemResponse struct {
	ID     int `json:"id"`
	Fields struct {
		Title              string `json:"System.Title"`
		State              string `json:"System.State"`
		Reason             string `json:"System.Reason"`
		Type               string `json:"System.WorkItemType"`
		Project            string `json:"System.TeamProject"`
		Tags               string `json:"System.Tags"`
		Description        string `json:"System.Description"`
		ReproSteps         string `json:"Microsoft.VSTS.TCM.ReproSteps"`
		AcceptanceCriteria string `json:"Microsoft.VSTS.Common.AcceptanceCriteria"`
		Priority           *int   `json:"Microsoft.VSTS.Common.Priority"`
	} `json:"fields"`
	Relations []struct {
		Rel string `json:"rel"`
		URL string `json:"url"`
	} `json:"relations"`
}

// toWorkItem maps the raw REST payload onto the WorkItem the tracker consumes,
// rendering the rich-text body to markdown and splitting the relations into
// parent, children and blockers.
func (r *workItemResponse) toWorkItem() WorkItem {
	item := WorkItem{
		ID:          r.ID,
		Title:       r.Fields.Title,
		Description: describe(r.Fields.Description, r.Fields.ReproSteps, r.Fields.AcceptanceCriteria),
		State:       r.Fields.State,
		Reason:      r.Fields.Reason,
		Type:        r.Fields.Type,
		Project:     r.Fields.Project,
		Tags:        ParseTags(r.Fields.Tags),
		Priority:    priorityUnset,
	}
	if p := r.Fields.Priority; p != nil {
		item.Priority = *p
	}
	for _, rel := range r.Relations {
		id, ok := relationID(rel.URL)
		if !ok {
			continue
		}
		switch rel.Rel {
		case relParent:
			item.Parent = id
		case relChild:
			item.Children = append(item.Children, id)
		case relPredecessor:
			item.BlockedBy = append(item.BlockedBy, id)
		}
	}
	return item
}

// describe assembles the markdown body the build prompt receives. Most
// work-item types keep their body in System.Description; a Bug keeps it in
// ReproSteps instead, so whichever is populated wins, and the acceptance
// criteria are appended as their own section.
func describe(description, reproSteps, acceptance string) string {
	body := htmlToMarkdown(description)
	if body == "" {
		body = htmlToMarkdown(reproSteps)
	}
	criteria := htmlToMarkdown(acceptance)
	if criteria == "" {
		return body
	}
	if body == "" {
		return "## Acceptance criteria\n\n" + criteria
	}
	return body + "\n\n## Acceptance criteria\n\n" + criteria
}

// relationID extracts the work-item id a relation URL ends in. Relations to
// non-work-item artifacts (attachments, commits) carry a non-numeric tail and
// are reported as absent.
func relationID(rawURL string) (int, bool) {
	tail := rawURL[strings.LastIndex(rawURL, "/")+1:]
	id, err := strconv.Atoi(tail)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ParseTags splits the semicolon-delimited System.Tags string into tag names.
// Azure DevOps stores tags as one flat string, so this is also the shape a tag
// write has to rebuild.
func ParseTags(s string) []string {
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if tag := strings.TrimSpace(p); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// JoinTags renders tag names back into the System.Tags wire format.
func JoinTags(tags []string) string { return strings.Join(tags, "; ") }

func joinInts(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}
