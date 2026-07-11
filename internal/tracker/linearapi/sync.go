package linearapi

import "context"

// SyncIssue is one issue pulled for the hub's local store: the full content trau
// keeps as its working copy, including the description, comments, and timestamps
// the lighter Issue/BacklogIssue reads omit.
type SyncIssue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	Priority    int
	DueDate     string
	URL         string
	CreatedAt   string
	UpdatedAt   string
	State       State
	Project     Project
	Parent      string
	Labels      []Label
	HasChildren bool
	Comments    []Comment
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
func (c *Client) ProjectIssues(ctx context.Context, teamID, projectID string) ([]SyncIssue, error) {
	if c.apiKey == "" {
		return nil, ErrNotEnabled
	}
	filter := issueFilter(teamID, projectID)
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

func issueFilter(teamID, projectID string) map[string]any {
	if projectID != "" {
		return map[string]any{"project": map[string]any{"id": map[string]any{"eq": projectID}}}
	}
	if teamID != "" {
		return map[string]any{"team": map[string]any{"id": map[string]any{"eq": teamID}}}
	}
	return nil
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
	ID          string      `json:"id"`
	Identifier  string      `json:"identifier"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Priority    int         `json:"priority"`
	DueDate     string      `json:"dueDate"`
	URL         string      `json:"url"`
	CreatedAt   string      `json:"createdAt"`
	UpdatedAt   string      `json:"updatedAt"`
	State       stateNode   `json:"state"`
	Project     projectNode `json:"project"`
	Parent      struct {
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
