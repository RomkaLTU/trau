package linearapi

import (
	"context"
	"strings"
	"time"
)

// SyncIssue is one issue pulled for the hub's local store: the full content trau
// keeps as its working copy, including the description, comments, and timestamps
// the lighter Issue/BacklogIssue reads omit.
type SyncIssue struct {
	ID           string
	Identifier   string
	Title        string
	Description  string
	Priority     int
	DueDate      string
	URL          string
	CreatedAt    string
	UpdatedAt    string
	State        State
	Project      Project
	Parent       string
	Labels       []Label
	HasChildren  bool
	AssigneeID   string
	AssigneeName string
	Comments     []Comment
}

// Comment is one comment on an issue, keyed by its node id. Author is the
// commenter's display name, empty for a bot or system comment.
type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt string
	UpdatedAt string
}

// syncMaxPages bounds the cursor loop so a huge project cannot spin the pull
// indefinitely.
const syncMaxPages = 100

// ProjectIssues pulls every issue in a project (when projectID is set) or a whole
// team (when only teamID is set) with the full content sync needs, through one
// server-side filter — never a per-issue fetch. It pages the cursor to the end.
// A non-empty since narrows the server-side filter to issues updated after that
// tracker timestamp, so an incremental sync fetches only what changed; a cursor
// Linear cannot parse falls back to a full pull.
func (c *Client) ProjectIssues(ctx context.Context, teamID, projectID, since string) ([]SyncIssue, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	filter := issueFilter(teamID, projectID, since)
	if filter == nil {
		return nil, ErrNotEnabled
	}
	var out []SyncIssue
	after := ""
	for page := 0; page < syncMaxPages; page++ {
		vars := map[string]any{"filter": filter}
		if after != "" {
			vars["after"] = after
		}
		var dst syncQueryResponse
		if err := c.do(ctx, syncQuery, vars, &dst); err != nil {
			return nil, err
		}
		for i := range dst.Data.Issues.Nodes {
			out = append(out, dst.Data.Issues.Nodes[i].toSyncIssue())
		}
		if !dst.Data.Issues.PageInfo.HasNextPage || dst.Data.Issues.PageInfo.EndCursor == "" {
			break
		}
		after = dst.Data.Issues.PageInfo.EndCursor
	}
	return out, nil
}

// ProjectIssueIDs returns just the human identifiers of every issue in a project
// (or a whole team when only teamID is set), paging the cursor to the end. It is
// the cheap full-set fetch a reconciliation sweep diffs against the local store —
// identifier-only, never the full-content ProjectIssues pull.
func (c *Client) ProjectIssueIDs(ctx context.Context, teamID, projectID string) ([]string, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	filter := issueFilter(teamID, projectID, "")
	if filter == nil {
		return nil, ErrNotEnabled
	}
	var out []string
	after := ""
	for page := 0; page < syncMaxPages; page++ {
		vars := map[string]any{"filter": filter}
		if after != "" {
			vars["after"] = after
		}
		var dst identifiersResponse
		if err := c.do(ctx, identifiersQuery, vars, &dst); err != nil {
			return nil, err
		}
		for _, n := range dst.Data.Issues.Nodes {
			if n.Identifier != "" {
				out = append(out, n.Identifier)
			}
		}
		if !dst.Data.Issues.PageInfo.HasNextPage || dst.Data.Issues.PageInfo.EndCursor == "" {
			break
		}
		after = dst.Data.Issues.PageInfo.EndCursor
	}
	return out, nil
}

type identifiersResponse struct {
	Data struct {
		Issues struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				Identifier string `json:"identifier"`
			} `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
}

func issueFilter(teamID, projectID, since string) map[string]any {
	var filter map[string]any
	switch {
	case projectID != "":
		filter = map[string]any{"project": map[string]any{"id": map[string]any{"eq": projectID}}}
	case teamID != "":
		filter = map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	default:
		return nil
	}
	if s := updatedSince(since); s != "" {
		filter["updatedAt"] = map[string]any{"gt": s}
	}
	return filter
}

// updatedSince validates a stored cursor against the timestamp shape Linear's
// `updatedAt` returns before it is used as a server-side `gt` filter. An empty or
// unparseable cursor yields "" so the pull falls back to a full project rather
// than sending an uncoercible value the API rejects on every subsequent tick.
func updatedSince(since string) string {
	since = strings.TrimSpace(since)
	if since == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		return ""
	}
	return since
}

type syncQueryResponse struct {
	Data struct {
		Issues struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []syncNode `json:"nodes"`
		} `json:"issues"`
	} `json:"data"`
}

type syncNode struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	DueDate     string    `json:"dueDate"`
	URL         string    `json:"url"`
	CreatedAt   string    `json:"createdAt"`
	UpdatedAt   string    `json:"updatedAt"`
	State       stateNode `json:"state"`
	Assignee    *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"assignee"`
	Project projectNode `json:"project"`
	Parent  struct {
		Identifier string `json:"identifier"`
	} `json:"parent"`
	Labels struct {
		Nodes []labelNode `json:"nodes"`
	} `json:"labels"`
	Children struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	} `json:"children"`
	Comments struct {
		Nodes []commentNode `json:"nodes"`
	} `json:"comments"`
}

type commentNode struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	User      *struct {
		Name string `json:"name"`
	} `json:"user"`
}

func (n *syncNode) toSyncIssue() SyncIssue {
	iss := SyncIssue{
		ID:          n.ID,
		Identifier:  n.Identifier,
		Title:       n.Title,
		Description: n.Description,
		Priority:    n.Priority,
		DueDate:     n.DueDate,
		URL:         n.URL,
		CreatedAt:   n.CreatedAt,
		UpdatedAt:   n.UpdatedAt,
		State:       State(n.State),
		Project:     Project(n.Project),
		Parent:      n.Parent.Identifier,
		HasChildren: len(n.Children.Nodes) > 0,
	}
	if n.Assignee != nil {
		iss.AssigneeID = n.Assignee.ID
		iss.AssigneeName = n.Assignee.Name
	}
	for _, l := range n.Labels.Nodes {
		iss.Labels = append(iss.Labels, Label(l))
	}
	for _, cm := range n.Comments.Nodes {
		c := Comment{ID: cm.ID, Body: cm.Body, CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt}
		if cm.User != nil {
			c.Author = cm.User.Name
		}
		iss.Comments = append(iss.Comments, c)
	}
	return iss
}
