package jiraapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// SyncIssue is one issue pulled for the hub's local store: the full content trau
// keeps as its working copy, including the description and comments the lighter
// BacklogIssue read omits.
type SyncIssue struct {
	Key         string
	Summary     string
	Description string
	Status      Status
	Resolution  string
	Priority    int
	DueDate     string
	Parent      string
	Labels      []string
	IsEpic      bool
	Created     string
	Updated     string
	Comments    []Comment
}

// Comment is one comment on an issue, keyed by its Jira id. Author is the
// commenter's display name.
type Comment struct {
	ID      string
	Author  string
	Body    string
	Created string
	Updated string
}

// syncFields is the field set the sync pull requests; /search/jql returns ID-only
// issues without it.
var syncFields = []string{
	"summary", "description", "status", "resolution", "priority", "duedate",
	"parent", "labels", "issuetype", "created", "updated", "comment",
}

// SyncIssues pulls every issue in a project with the full content sync needs —
// description and comments — through a server-side JQL project filter, paging the
// nextPageToken to the end. It needs a project key; an empty one yields
// ErrNotEnabled rather than a project-less query.
func (c *Client) SyncIssues(ctx context.Context, project string) ([]SyncIssue, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrNotEnabled
	}
	jql := "project = " + jqlQuote(project) + " ORDER BY updated DESC"
	var out []SyncIssue
	token := ""
	for page := 0; page < searchMaxPages; page++ {
		body, err := json.Marshal(searchRequest{
			JQL:           jql,
			Fields:        syncFields,
			MaxResults:    searchPageSize,
			NextPageToken: token,
		})
		if err != nil {
			return nil, err
		}
		var resp syncSearchResponse
		if err := c.do(ctx, http.MethodPost, searchPath, body, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Issues {
			out = append(out, resp.Issues[i].toSyncIssue())
		}
		if strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

type syncSearchResponse struct {
	Issues        []syncSearchIssue `json:"issues"`
	NextPageToken string            `json:"nextPageToken"`
}

type syncSearchIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Status      *statusField    `json:"status"`
		Resolution  *struct {
			Name string `json:"name"`
		} `json:"resolution"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		DueDate string `json:"duedate"`
		Parent  *struct {
			Key string `json:"key"`
		} `json:"parent"`
		Labels    []string `json:"labels"`
		IssueType *struct {
			HierarchyLevel int `json:"hierarchyLevel"`
		} `json:"issuetype"`
		Created string `json:"created"`
		Updated string `json:"updated"`
		Comment *struct {
			Comments []struct {
				ID     string `json:"id"`
				Author *struct {
					DisplayName string `json:"displayName"`
				} `json:"author"`
				Body    json.RawMessage `json:"body"`
				Created string          `json:"created"`
				Updated string          `json:"updated"`
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

func (r *syncSearchIssue) toSyncIssue() SyncIssue {
	iss := SyncIssue{
		Key:         r.Key,
		Summary:     r.Fields.Summary,
		Description: adfToText(r.Fields.Description),
		DueDate:     r.Fields.DueDate,
		Labels:      r.Fields.Labels,
		Created:     r.Fields.Created,
		Updated:     r.Fields.Updated,
	}
	if s := r.Fields.Status; s != nil {
		iss.Status = Status{Name: s.Name, Category: s.StatusCategory.Key}
	}
	if res := r.Fields.Resolution; res != nil {
		iss.Resolution = res.Name
	}
	if p := r.Fields.Priority; p != nil {
		iss.Priority = mapPriority(p.Name)
	}
	if p := r.Fields.Parent; p != nil {
		iss.Parent = p.Key
	}
	if it := r.Fields.IssueType; it != nil {
		iss.IsEpic = it.HierarchyLevel > 0
	}
	if cm := r.Fields.Comment; cm != nil {
		for _, c := range cm.Comments {
			comment := Comment{ID: c.ID, Body: adfToText(c.Body), Created: c.Created, Updated: c.Updated}
			if c.Author != nil {
				comment.Author = c.Author.DisplayName
			}
			iss.Comments = append(iss.Comments, comment)
		}
	}
	return iss
}

// mapPriority buckets a Jira priority name onto the same 1-5 scale (most urgent
// first) the Linear priority uses, so the store carries a comparable integer. An
// unknown or unset priority is 0.
func mapPriority(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "highest":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	case "low":
		return 4
	case "lowest":
		return 5
	default:
		return 0
	}
}
