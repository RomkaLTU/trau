package jiraapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// searchPath is the token-paginated JQL search endpoint. The legacy /search was
// removed 2025-05-01; /search/jql pages with nextPageToken (not startAt) and
// returns ID-only issues unless the fields are requested explicitly.
const searchPath = "/search/jql"

const (
	// searchPageSize caps issues per page.
	searchPageSize = 100
	// searchMaxPages bounds the pagination loop so a huge or misbehaving result
	// set can't spin indefinitely.
	searchMaxPages = 100
)

// eligibleFields and childFields are the field sets each search needs;
// /search/jql returns ID-only issues without them.
var (
	eligibleFields = []string{"summary", "status", "issuetype", "issuelinks", "labels"}
	childFields    = []string{"summary", "status", "issuetype", "subtasks"}
	backlogFields  = []string{"summary", "status", "issuetype", "labels", "parent", "resolution"}
)

// Candidate is one issue from the eligibility search. It is returned in JQL order;
// the tracker applies the remaining selection policy (epic exclusion, blocker
// resolution, prefix match) over these.
type Candidate struct {
	Key        string
	Summary    string
	StatusName string
	IsEpic     bool // issuetype.hierarchyLevel > 0 — a container, never a buildable leaf
	Labels     []string
	BlockedBy  []Blocker
}

// Blocker is an issue linked to a candidate by "is blocked by". Resolved is true
// once the blocker reaches the done status category (completed or canceled), so
// it no longer holds the candidate back.
type Blocker struct {
	Key      string
	Resolved bool
}

// Child is one direct child of a parent issue — a sub-task or an epic-child. Done
// marks a child already in a done status category; HasChildren flags a nested
// parent/epic so the loop never descends into it as a leaf.
type Child struct {
	Key         string
	Summary     string
	Done        bool
	HasChildren bool
}

// Eligible returns the ready-queue candidates for a project, ordered by the loop's
// selection rules (priority, then soonest due date, then lowest key). It needs a
// project key; an empty one yields ErrNotEnabled so the caller falls back to the
// MCP rather than issuing a project-less query.
func (c *Client) Eligible(ctx context.Context, project, readyLabel string) ([]Candidate, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrNotEnabled
	}
	raws, err := c.search(ctx, eligibleJQL(project, readyLabel), eligibleFields)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(raws))
	for i := range raws {
		out = append(out, raws[i].toCandidate())
	}
	return out, nil
}

// SubIssues returns the direct children of parentKey through the unified parent
// field, so it captures both sub-tasks and epic-children (fields.subtasks alone
// misses stories under an epic).
func (c *Client) SubIssues(ctx context.Context, parentKey string) ([]Child, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	parentKey = strings.TrimSpace(parentKey)
	if parentKey == "" {
		return nil, ErrNotFound
	}
	jql := "parent = " + jqlQuote(parentKey) + " ORDER BY key ASC"
	raws, err := c.search(ctx, jql, childFields)
	if err != nil {
		return nil, err
	}
	out := make([]Child, 0, len(raws))
	for i := range raws {
		out = append(out, raws[i].toChild())
	}
	return out, nil
}

// BacklogIssue is one issue in a project's full backlog, carrying the fields the
// backlog board needs: display status and its stable category, resolution (to
// tell a canceled done-issue from a completed one), epic-type flag, epic parent,
// and labels.
type BacklogIssue struct {
	Key            string
	Summary        string
	StatusName     string
	StatusCategory string
	Resolution     string
	IsEpic         bool
	ParentKey      string
	Labels         []string
}

// Backlog returns every issue in a project, ordered newest first, applying no
// label or status filter — the backlog board shows the whole project, not just
// the ready queue. It needs a project key; an empty one yields ErrNotEnabled so
// the caller falls back to the MCP rather than issuing a project-less query.
func (c *Client) Backlog(ctx context.Context, project string) ([]BacklogIssue, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrNotEnabled
	}
	jql := "project = " + jqlQuote(project) + " ORDER BY created DESC"
	raws, err := c.search(ctx, jql, backlogFields)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogIssue, 0, len(raws))
	for i := range raws {
		out = append(out, raws[i].toBacklog())
	}
	return out, nil
}

// eligibleJQL builds the ready-queue query: the project's issues that carry the
// ready label, sit in the To-Do status category (unstarted — not In Progress or
// Done), and are unresolved, ordered by priority, soonest due date, then key.
func eligibleJQL(project, readyLabel string) string {
	jql := "project = " + jqlQuote(project)
	if label := strings.TrimSpace(readyLabel); label != "" {
		jql += " AND labels = " + jqlQuote(label)
	}
	jql += ` AND statusCategory = "To Do" AND resolution = EMPTY`
	jql += " ORDER BY priority DESC, duedate ASC, key ASC"
	return jql
}

// jqlQuote wraps a value in double quotes for a JQL clause, escaping backslashes
// and quotes so a label or key containing one can't break out of the string.
func jqlQuote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// search runs a JQL query, following nextPageToken pagination until the last page
// (or searchMaxPages) and accumulating every issue.
func (c *Client) search(ctx context.Context, jql string, fields []string) ([]searchIssue, error) {
	var all []searchIssue
	token := ""
	for page := 0; page < searchMaxPages; page++ {
		body, err := json.Marshal(searchRequest{
			JQL:           jql,
			Fields:        fields,
			MaxResults:    searchPageSize,
			NextPageToken: token,
		})
		if err != nil {
			return nil, err
		}
		var resp searchResponse
		if err := c.do(ctx, http.MethodPost, searchPath, body, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Issues...)
		if strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		token = resp.NextPageToken
	}
	return all, nil
}

type searchRequest struct {
	JQL           string   `json:"jql"`
	Fields        []string `json:"fields"`
	MaxResults    int      `json:"maxResults"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
}

type searchResponse struct {
	Issues        []searchIssue `json:"issues"`
	NextPageToken string        `json:"nextPageToken"`
}

type searchIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary   string       `json:"summary"`
		Status    *statusField `json:"status"`
		IssueType *struct {
			HierarchyLevel int `json:"hierarchyLevel"`
		} `json:"issuetype"`
		Subtasks   []json.RawMessage `json:"subtasks"`
		IssueLinks []issueLink       `json:"issuelinks"`
		Labels     []string          `json:"labels"`
		Parent     *struct {
			Key string `json:"key"`
		} `json:"parent"`
		Resolution *struct {
			Name string `json:"name"`
		} `json:"resolution"`
	} `json:"fields"`
}

type statusField struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

type issueLink struct {
	Type struct {
		Name   string `json:"name"`
		Inward string `json:"inward"`
	} `json:"type"`
	InwardIssue *linkedIssue `json:"inwardIssue"`
}

type linkedIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Status statusField `json:"status"`
	} `json:"fields"`
}

func (r *searchIssue) toCandidate() Candidate {
	cand := Candidate{Key: r.Key, Summary: r.Fields.Summary}
	if s := r.Fields.Status; s != nil {
		cand.StatusName = s.Name
	}
	if it := r.Fields.IssueType; it != nil {
		cand.IsEpic = it.HierarchyLevel > 0
	}
	cand.Labels = r.Fields.Labels
	cand.BlockedBy = blockersFromLinks(r.Fields.IssueLinks)
	return cand
}

func (r *searchIssue) toChild() Child {
	ch := Child{Key: r.Key, Summary: r.Fields.Summary}
	if s := r.Fields.Status; s != nil {
		ch.Done = strings.EqualFold(s.StatusCategory.Key, "done")
	}
	if len(r.Fields.Subtasks) > 0 {
		ch.HasChildren = true
	}
	if it := r.Fields.IssueType; it != nil && it.HierarchyLevel > 0 {
		ch.HasChildren = true
	}
	return ch
}

func (r *searchIssue) toBacklog() BacklogIssue {
	b := BacklogIssue{Key: r.Key, Summary: r.Fields.Summary, Labels: r.Fields.Labels}
	if s := r.Fields.Status; s != nil {
		b.StatusName = s.Name
		b.StatusCategory = s.StatusCategory.Key
	}
	if it := r.Fields.IssueType; it != nil {
		b.IsEpic = it.HierarchyLevel > 0
	}
	if p := r.Fields.Parent; p != nil {
		b.ParentKey = p.Key
	}
	if res := r.Fields.Resolution; res != nil {
		b.Resolution = res.Name
	}
	return b
}

// blockersFromLinks extracts the "is blocked by" links: for those the blocking
// issue appears as inwardIssue, and its done status category means the blocker no
// longer holds. Other link types (relates to, causes, …) are ignored.
func blockersFromLinks(links []issueLink) []Blocker {
	var out []Blocker
	for _, link := range links {
		if link.InwardIssue == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(link.Type.Inward), "blocked by") {
			continue
		}
		out = append(out, Blocker{
			Key:      link.InwardIssue.Key,
			Resolved: strings.EqualFold(link.InwardIssue.Fields.Status.StatusCategory.Key, "done"),
		})
	}
	return out
}
