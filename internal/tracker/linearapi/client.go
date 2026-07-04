// Package linearapi is a small, pragmatic GraphQL client for Linear.
// It covers the fast read/write operations the Trau loop performs frequently
// (title lookup, status moves, label changes, team listing) so they do not need
// an expensive MCP/agent round-trip. Complex operations that read files or need
// reasoning still go through the Linear MCP.
package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Common errors the tracker can use to decide whether to fall back to MCP.
var (
	ErrNotFound     = errors.New("linear: issue not found")
	ErrUnauthorized = errors.New("linear: unauthorized")
	ErrNotEnabled   = errors.New("linear: direct API not enabled")
)

// Client talks to Linear's GraphQL API.
type Client struct {
	apiKey string
	http   *http.Client
	// Endpoint overrides the GraphQL endpoint when non-empty; the zero value
	// targets Linear's public API.
	Endpoint string
}

// New returns a client that uses apiKey. An empty apiKey makes every method
// return ErrNotEnabled so callers can fall back to MCP.
func New(apiKey string) *Client {
	return &Client{
		apiKey: strings.TrimSpace(apiKey),
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Issue is the subset of a Linear issue the tracker consumes.
type Issue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	Priority    int
	DueDate     string
	State       State
	Team        Team
	Project     Project
	Parent      IssueRef
	Labels      []Label
	Children    []IssueRef
	BlockedBy   []IssueRef
}

// Project is a Linear project. Name is empty when the issue belongs to no project.
type Project struct {
	ID   string
	Name string
}

// State is a workflow state.
type State struct {
	ID   string
	Name string
	Type string
}

// Team is a Linear team.
type Team struct {
	ID   string
	Key  string
	Name string
}

// Label is an issue label.
type Label struct {
	ID   string
	Name string
}

// IssueRef is a lightweight issue reference.
type IssueRef struct {
	ID          string
	Identifier  string
	Title       string
	Priority    int
	DueDate     string
	State       State
	HasChildren bool // true when the referenced issue has its own sub-issues
}

// IsUnstarted reports whether the issue is in a backlog or unstarted state.
func (s State) IsUnstarted() bool {
	switch s.Type {
	case "backlog", "unstarted":
		return true
	}
	return false
}

// IsCompleted reports whether the issue is in a completed state.
func (s State) IsCompleted() bool {
	return s.Type == "completed"
}

// IsTerminal reports whether the issue has reached a finished state — completed
// or canceled. The epic preview uses it to flag sub-issues that will not run.
func (s State) IsTerminal() bool {
	return s.Type == "completed" || s.Type == "canceled"
}

// Issue fetches a single issue by its human-readable identifier (e.g. "COD-578").
func (c *Client) Issue(ctx context.Context, identifier string) (*Issue, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	teamKey, number, ok := splitIdentifier(identifier)
	if !ok {
		return nil, ErrNotFound
	}
	var dst issueQueryResponse
	if err := c.do(ctx, issueQuery, map[string]any{"number": number, "teamKey": teamKey}, &dst); err != nil {
		return nil, err
	}
	if len(dst.Data.Issues.Nodes) == 0 {
		return nil, ErrNotFound
	}
	return nodeToIssue(&dst.Data.Issues.Nodes[0]), nil
}

// splitIdentifier breaks a human issue id ("COD-493") into its team key ("COD") and
// number (493). It reports ok=false for anything that is not <KEY>-<N>.
func splitIdentifier(identifier string) (teamKey string, number float64, ok bool) {
	idx := strings.LastIndex(identifier, "-")
	if idx <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(identifier[idx+1:]))
	if err != nil {
		return "", 0, false
	}
	return identifier[:idx], float64(n), true
}

// PickCandidate is an issue returned by Pick.
type PickCandidate struct {
	Issue
	BlockedBy []IssueRef
}

// Pick returns ready issues for the team, sorted by the loop's selection rules:
// priority (urgent > high > medium > low), then due date (sooner first), then
// lowest issue number. The caller filters by state and blockers.
func (c *Client) Pick(ctx context.Context, teamID, readyLabel string) ([]PickCandidate, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	var dst pickQueryResponse
	if err := c.do(ctx, pickQuery, map[string]any{"teamId": teamID, "labelName": readyLabel}, &dst); err != nil {
		return nil, err
	}
	out := make([]PickCandidate, 0, len(dst.Data.Issues.Nodes))
	for i := range dst.Data.Issues.Nodes {
		out = append(out, nodeToPickCandidate(&dst.Data.Issues.Nodes[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		return runOrderLess(out[i].Priority, out[j].Priority, out[i].DueDate, out[j].DueDate, out[i].Identifier, out[j].Identifier)
	})
	return out, nil
}

// runOrderLess reports whether issue a should be selected before issue b under
// the loop's rules: priority (urgent > … > low; "no priority" sorts last), then
// due date (sooner first), then lowest issue number. It is the single comparator
// behind both the team-queue picker and the epic sub-issue preview.
func runOrderLess(pa, pb int, da, db, ida, idb string) bool {
	if pa == 0 && pb != 0 {
		return false
	}
	if pb == 0 && pa != 0 {
		return true
	}
	if pa != pb {
		return pa < pb
	}
	if da != db {
		return da < db
	}
	return issueNumber(ida) < issueNumber(idb)
}

// SortChildrenForRun orders an epic's sub-issues in place by the same rules the
// picker uses, so a preview lists them in approximately the order they will run.
func SortChildrenForRun(refs []IssueRef) {
	sort.Slice(refs, func(i, j int) bool {
		return runOrderLess(refs[i].Priority, refs[j].Priority, refs[i].DueDate, refs[j].DueDate, refs[i].Identifier, refs[j].Identifier)
	})
}

// issueNumber returns the numeric suffix of an identifier like "COD-578".
func issueNumber(id string) int {
	idx := strings.LastIndex(id, "-")
	if idx < 0 {
		return 0
	}
	n, _ := strconv.Atoi(id[idx+1:])
	return n
}

// ListTeams returns all teams visible to the API key.
func (c *Client) ListTeams(ctx context.Context) ([]Team, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	var dst teamsQueryResponse
	if err := c.do(ctx, teamsQuery, nil, &dst); err != nil {
		return nil, err
	}
	out := make([]Team, 0, len(dst.Data.Teams.Nodes))
	for _, n := range dst.Data.Teams.Nodes {
		out = append(out, Team(n))
	}
	return out, nil
}

// TeamByKey looks up a team by its key (e.g. "COD").
func (c *Client) TeamByKey(ctx context.Context, key string) (*Team, error) {
	teams, err := c.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	for _, t := range teams {
		if strings.ToUpper(t.Key) == key {
			return &t, nil
		}
	}
	return nil, ErrNotFound
}

// SetStatus moves the issue to the named workflow state and updates its label set.
// If stateName is empty, only labels are changed. If labelNames is nil, labels are
// left unchanged; if non-nil, the issue's labels are replaced with that exact set.
func (c *Client) SetStatus(ctx context.Context, identifier, stateName string, labelNames []string) error {
	if c.apiKey == "" {
		return ErrNotEnabled
	}
	issue, err := c.Issue(ctx, identifier)
	if err != nil {
		return err
	}

	var stateID string
	if stateName != "" {
		states, err := c.workflowStates(ctx, issue.Team.ID)
		if err != nil {
			return err
		}
		stateName = strings.ToLower(strings.TrimSpace(stateName))
		for _, s := range states {
			if strings.ToLower(strings.TrimSpace(s.Name)) == stateName {
				stateID = s.ID
				break
			}
		}
		if stateID == "" {
			return fmt.Errorf("linear: no workflow state named %q in team %s", stateName, issue.Team.Key)
		}
	}

	var labelIDs []string
	if labelNames != nil {
		teamLabels, err := c.teamLabels(ctx, issue.Team.ID)
		if err != nil {
			return err
		}
		for _, name := range labelNames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			id, ok := teamLabels[name]
			if !ok {
				return fmt.Errorf("linear: label %q does not exist in team %s", name, issue.Team.Key)
			}
			labelIDs = append(labelIDs, id)
		}
	}

	vars := map[string]any{"id": issue.ID}
	if stateID != "" {
		vars["stateId"] = stateID
	}
	if labelNames != nil {
		vars["labelIds"] = labelIDs
	}

	var dst issueUpdateResponse
	return c.do(ctx, issueUpdateMutation, vars, &dst)
}

// AddComment adds a comment to the issue.
func (c *Client) AddComment(ctx context.Context, identifier, body string) error {
	if c.apiKey == "" {
		return ErrNotEnabled
	}
	issue, err := c.Issue(ctx, identifier)
	if err != nil {
		return err
	}
	var dst commentCreateResponse
	return c.do(ctx, commentCreateMutation, map[string]any{"issueId": issue.ID, "body": body}, &dst)
}

// Labels returns a name->id map of the labels defined in the team.
func (c *Client) Labels(ctx context.Context, teamID string) (map[string]string, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	return c.teamLabels(ctx, teamID)
}

// EnsureLabel creates a label in the team if it does not already exist.
func (c *Client) EnsureLabel(ctx context.Context, teamID, name string) error {
	if c.apiKey == "" {
		return ErrNotEnabled
	}
	labels, err := c.teamLabels(ctx, teamID)
	if err != nil {
		return err
	}
	if _, ok := labels[name]; ok {
		return nil
	}
	var dst issueLabelCreateResponse
	return c.do(ctx, issueLabelCreateMutation, map[string]any{"name": name, "teamId": teamID}, &dst)
}

// CreateIssueInput describes an issue to create. TeamID is required; Labels are
// resolved by name against the team's label set (unknown names skipped); ParentID
// nests the issue under an epic and ProjectID places it in a project, both omitted
// when empty.
type CreateIssueInput struct {
	TeamID      string
	Title       string
	Description string
	Labels      []string
	ParentID    string
	ProjectID   string
}

// CreateIssue creates a new issue and returns its identifier and URL. Label names
// that do not exist in the team are dropped — Linear can only attach labels that
// already exist.
func (c *Client) CreateIssue(ctx context.Context, in CreateIssueInput) (identifier, url string, err error) {
	if c.apiKey == "" {
		return "", "", ErrNotEnabled
	}
	labels, err := c.teamLabels(ctx, in.TeamID)
	if err != nil {
		return "", "", err
	}
	var labelIDs []string
	for _, name := range in.Labels {
		if id, ok := labels[name]; ok {
			labelIDs = append(labelIDs, id)
		}
	}
	vars := map[string]any{
		"teamId":      in.TeamID,
		"title":       in.Title,
		"description": in.Description,
		"labelIds":    labelIDs,
	}
	if in.ParentID != "" {
		vars["parentId"] = in.ParentID
	}
	if in.ProjectID != "" {
		vars["projectId"] = in.ProjectID
	}
	var dst issueCreateResponse
	if err := c.do(ctx, issueCreateMutation, vars, &dst); err != nil {
		return "", "", err
	}
	if dst.Data.IssueCreate.Issue.Identifier == "" {
		return "", "", errors.New("linear: create issue returned no identifier")
	}
	return dst.Data.IssueCreate.Issue.Identifier, dst.Data.IssueCreate.Issue.URL, nil
}

// CreateDocument creates a document under a project from markdown content and
// returns its URL. content is stored verbatim, so the markdown round-trips.
func (c *Client) CreateDocument(ctx context.Context, projectID, title, content string) (string, error) {
	if c.apiKey == "" {
		return "", ErrNotEnabled
	}
	vars := map[string]any{"projectId": projectID, "title": title, "content": content}
	var dst documentCreateResponse
	if err := c.do(ctx, documentCreateMutation, vars, &dst); err != nil {
		return "", err
	}
	if dst.Data.DocumentCreate.Document.URL == "" {
		return "", errors.New("linear: create document returned no url")
	}
	return dst.Data.DocumentCreate.Document.URL, nil
}

// ProjectByName resolves a project by its exact name (case-insensitive) and returns
// it, or ErrNotFound when no project matches.
func (c *Client) ProjectByName(ctx context.Context, name string) (*Project, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	name = strings.TrimSpace(name)
	var dst projectsQueryResponse
	if err := c.do(ctx, projectsByNameQuery, map[string]any{"name": name}, &dst); err != nil {
		return nil, err
	}
	for _, n := range dst.Data.Projects.Nodes {
		if strings.EqualFold(strings.TrimSpace(n.Name), name) {
			return &Project{ID: n.ID, Name: n.Name}, nil
		}
	}
	return nil, ErrNotFound
}

// workflowStates returns the workflow states for a team.
func (c *Client) workflowStates(ctx context.Context, teamID string) ([]State, error) {
	var dst workflowStatesQueryResponse
	if err := c.do(ctx, workflowStatesQuery, map[string]any{"teamId": teamID}, &dst); err != nil {
		return nil, err
	}
	out := make([]State, 0, len(dst.Data.WorkflowStates.Nodes))
	for _, n := range dst.Data.WorkflowStates.Nodes {
		out = append(out, State(n))
	}
	return out, nil
}

// teamLabels returns a name->id map of labels for a team.
func (c *Client) teamLabels(ctx context.Context, teamID string) (map[string]string, error) {
	// Linear's label list is small per team; query labels directly.
	const q = `
query TeamLabels($teamId: ID!) {
  issueLabels(filter: { team: { id: { eq: $teamId } } }) {
    nodes {
      id
      name
    }
  }
}
`
	type node struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type resp struct {
		Data struct {
			IssueLabels struct {
				Nodes []node `json:"nodes"`
			} `json:"issueLabels"`
		} `json:"data"`
	}
	var dst resp
	if err := c.do(ctx, q, map[string]any{"teamId": teamID}, &dst); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(dst.Data.IssueLabels.Nodes))
	for _, n := range dst.Data.IssueLabels.Nodes {
		out[n.Name] = n.ID
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, dst any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return err
	}
	target := endpoint
	if c.Endpoint != "" {
		target = c.Endpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}

	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}

	var gr graphResponse
	if err := json.Unmarshal(resBody, &gr); err != nil {
		return fmt.Errorf("linear: invalid response (%d): %w", res.StatusCode, err)
	}
	if len(gr.Errors) > 0 {
		msgs := make([]string, 0, len(gr.Errors))
		for _, e := range gr.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("linear: HTTP %d: %s", res.StatusCode, string(resBody))
	}
	return json.Unmarshal(resBody, dst)
}

type graphResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Response wrappers for unmarshalling.

type issueQueryResponse struct {
	Data struct {
		Issues struct {
			Nodes []issueNode `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
}

type pickQueryResponse struct {
	Data struct {
		Issues struct {
			Nodes []pickNode `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
}

type teamsQueryResponse struct {
	Data struct {
		Teams struct {
			Nodes []teamNode `json:"nodes"`
		} `json:"teams"`
	} `json:"data"`
}

type projectsQueryResponse struct {
	Data struct {
		Projects struct {
			Nodes []projectNode `json:"nodes"`
		} `json:"projects"`
	} `json:"data"`
}

type workflowStatesQueryResponse struct {
	Data struct {
		WorkflowStates struct {
			Nodes []stateNode `json:"nodes"`
		} `json:"workflowStates"`
	} `json:"data"`
}

type issueUpdateResponse struct {
	Data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	} `json:"data"`
}

type commentCreateResponse struct {
	Data struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	} `json:"data"`
}

type issueLabelCreateResponse struct {
	Data struct {
		IssueLabelCreate struct {
			Success    bool `json:"success"`
			IssueLabel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	} `json:"data"`
}

type issueCreateResponse struct {
	Data struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"issue"`
		} `json:"issueCreate"`
	} `json:"data"`
}

type documentCreateResponse struct {
	Data struct {
		DocumentCreate struct {
			Success  bool `json:"success"`
			Document struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"document"`
		} `json:"documentCreate"`
	} `json:"data"`
}

// Raw nodes.

type teamNode struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type stateNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type projectNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type labelNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type issueRefNode struct {
	ID         string    `json:"id"`
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	Priority   int       `json:"priority"`
	DueDate    string    `json:"dueDate"`
	State      stateNode `json:"state"`
	Children   struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	} `json:"children"`
}

// relationNode is one IssueRelation. For "blocked by", we read inverseRelations
// whose type is "blocks" — there the `issue` is the blocker.
type relationNode struct {
	Type  string       `json:"type"`
	Issue issueRefNode `json:"issue"`
}

// blockers extracts the "blocked by" issues from a set of inverse relations.
func blockers(nodes []relationNode) []IssueRef {
	var out []IssueRef
	for _, rel := range nodes {
		if rel.Type != "blocks" {
			continue
		}
		out = append(out, IssueRef{ID: rel.Issue.ID, Identifier: rel.Issue.Identifier, State: State(rel.Issue.State)})
	}
	return out
}

type issueNode struct {
	ID          string      `json:"id"`
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Priority    int         `json:"priority"`
	DueDate     string      `json:"dueDate"`
	State       stateNode   `json:"state"`
	Team        teamNode    `json:"team"`
	Project     projectNode `json:"project"`
	Parent      struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
	} `json:"parent"`
	Labels struct {
		Nodes []labelNode `json:"nodes"`
	} `json:"labels"`
	Children struct {
		Nodes []issueRefNode `json:"nodes"`
	} `json:"children"`
	InverseRelations struct {
		Nodes []relationNode `json:"nodes"`
	} `json:"inverseRelations"`
}

type pickNode struct {
	ID         string      `json:"id"`
	Identifier string      `json:"identifier"`
	Title      string      `json:"title"`
	Priority   int         `json:"priority"`
	DueDate    string      `json:"dueDate"`
	State      stateNode   `json:"state"`
	Project    projectNode `json:"project"`
	Children   struct {
		Nodes []issueRefNode `json:"nodes"`
	} `json:"children"`
	InverseRelations struct {
		Nodes []relationNode `json:"nodes"`
	} `json:"inverseRelations"`
}

func nodeToIssue(n *issueNode) *Issue {
	issue := &Issue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		Priority:    n.Priority,
		DueDate:     n.DueDate,
		State:       State(n.State),
		Team:        Team(n.Team),
		Project:     Project(n.Project),
		Parent:      IssueRef{ID: n.Parent.ID, Identifier: n.Parent.Identifier},
	}
	for _, l := range n.Labels.Nodes {
		issue.Labels = append(issue.Labels, Label(l))
	}
	for _, s := range n.Children.Nodes {
		issue.Children = append(issue.Children, IssueRef{
			ID:          s.ID,
			Identifier:  s.Identifier,
			Title:       s.Title,
			Priority:    s.Priority,
			DueDate:     s.DueDate,
			State:       State(s.State),
			HasChildren: len(s.Children.Nodes) > 0,
		})
	}
	issue.BlockedBy = blockers(n.InverseRelations.Nodes)
	return issue
}

func nodeToPickCandidate(n *pickNode) PickCandidate {
	c := PickCandidate{
		Issue: Issue{
			ID:         n.ID,
			Identifier: n.Identifier,
			Title:      n.Title,
			Priority:   n.Priority,
			DueDate:    n.DueDate,
			State:      State{ID: n.State.ID, Name: n.State.Name, Type: n.State.Type},
			Project:    Project(n.Project),
		},
	}
	for _, s := range n.Children.Nodes {
		c.Children = append(c.Children, IssueRef{ID: s.ID, Identifier: s.Identifier, Title: s.Title})
	}
	c.BlockedBy = blockers(n.InverseRelations.Nodes)
	return c
}
