package jiraapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// SyncIssue is one issue pulled for the hub's local store: the full content trau
// keeps as its working copy, including the description and comments the lighter
// BacklogIssue read omits.
type SyncIssue struct {
	Key          string
	Summary      string
	Description  string
	Status       Status
	Resolution   string
	Priority     int
	DueDate      string
	Parent       string
	Labels       []string
	IsEpic       bool
	AssigneeID   string
	AssigneeName string
	Created      string
	Updated      string
	Comments     []Comment
	Attachments  []Attachment
}

// Attachment is one file attached to an issue. Content is the authenticated URL
// its bytes are served from — the same URL an embedded image in the description
// resolves to, so a file is registered once however it was referenced.
type Attachment struct {
	ID       string
	Filename string
	MimeType string
	Size     int64
	Content  string
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
	"parent", "labels", "issuetype", "assignee", "created", "updated", "comment",
	"attachment",
}

// SyncIssues pulls every issue in a project with the full content sync needs —
// description and comments — through a server-side JQL project filter, paging the
// nextPageToken to the end. It needs a project key; an empty one yields
// ErrNotEnabled rather than a project-less query. A non-empty since narrows the
// JQL to issues updated at or after that tracker timestamp, so an incremental
// sync fetches only what changed; a cursor Jira cannot parse falls back to a full
// pull.
func (c *Client) SyncIssues(ctx context.Context, project, since string) ([]SyncIssue, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, ErrNotEnabled
	}
	jql := "project = " + jqlQuote(project) + jqlUpdatedSince(since) + " ORDER BY updated DESC"
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

// jqlDateFormat is the minute-precision literal JQL compares dates against; Jira
// rejects the second/offset precision its REST timestamps carry.
const jqlDateFormat = "2006-01-02 15:04"

// jiraUpdatedLayouts are the timestamp shapes Jira's REST `updated` field comes
// back in, tried in order to parse a stored cursor.
var jiraUpdatedLayouts = []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700"}

// jqlUpdatedSince builds the incremental `AND updated >= "..."` clause from a
// stored cursor, keeping the cursor's own wall-clock so it matches the account
// timezone JQL evaluates the literal in. It compares at minute precision with
// `>=` so an issue on the boundary minute is re-fetched rather than skipped — the
// upsert is idempotent. An empty or unparseable cursor yields no clause, so the
// pull falls back to the full project.
func jqlUpdatedSince(since string) string {
	since = strings.TrimSpace(since)
	if since == "" {
		return ""
	}
	for _, layout := range jiraUpdatedLayouts {
		if t, err := time.Parse(layout, since); err == nil {
			return ` AND updated >= ` + jqlQuote(t.Format(jqlDateFormat))
		}
	}
	return ""
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
		Assignee *struct {
			AccountID   string `json:"accountId"`
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
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
		Attachment []attachmentField `json:"attachment"`
	} `json:"fields"`
}

type attachmentField struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Content  string `json:"content"`
}

func toAttachments(fields []attachmentField) []Attachment {
	out := make([]Attachment, 0, len(fields))
	for _, f := range fields {
		out = append(out, Attachment(f))
	}
	return out
}

func (r *syncSearchIssue) toSyncIssue() SyncIssue {
	files := toAttachments(r.Fields.Attachment)
	iss := SyncIssue{
		Key:         r.Key,
		Summary:     r.Fields.Summary,
		Description: adfToMarkdown(r.Fields.Description, files),
		DueDate:     r.Fields.DueDate,
		Labels:      r.Fields.Labels,
		Created:     r.Fields.Created,
		Updated:     r.Fields.Updated,
		Attachments: files,
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
	if a := r.Fields.Assignee; a != nil {
		iss.AssigneeID = a.AccountID
		iss.AssigneeName = a.DisplayName
	}
	if cm := r.Fields.Comment; cm != nil {
		for _, c := range cm.Comments {
			comment := Comment{ID: c.ID, Body: adfToMarkdown(c.Body, files), Created: c.Created, Updated: c.Updated}
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
