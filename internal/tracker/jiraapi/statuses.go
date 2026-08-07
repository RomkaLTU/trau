package jiraapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// ProjectStatuses returns every workflow status the project's issue types can
// reach, each carrying the statusCategory key trau groups by. Jira reports
// statuses per issue type — a Bug and a Story routinely share most of a workflow
// and differ in a step or two — so the answer is the union across issue types,
// deduped by status name, which is the vocabulary a status mapping keys on.
//
// Jira statuses are site-wide objects addressed by name, so the same name cannot
// carry two categories: the dedup collapses repetition rather than picking a
// winner.
func (c *Client) ProjectStatuses(ctx context.Context, projectKey string) ([]Status, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return nil, ErrNotFound
	}
	var resp []issueTypeStatuses
	if err := c.do(ctx, http.MethodGet, projectStatusesPath(projectKey), nil, &resp); err != nil {
		return nil, err
	}
	var out []Status
	seen := map[string]bool{}
	for _, typ := range resp {
		for _, st := range typ.Statuses {
			name := strings.TrimSpace(st.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Status{
				Name:     name,
				Category: strings.TrimSpace(st.StatusCategory.Key),
			})
		}
	}
	return out, nil
}

func projectStatusesPath(projectKey string) string {
	return "/project/" + url.PathEscape(projectKey) + "/statuses"
}

// issueTypeStatuses is one issue type's slice of the project's workflows, as
// GET /project/{projectIdOrKey}/statuses reports it: a top-level array of issue
// types, each carrying the statuses its own workflow can reach.
type issueTypeStatuses struct {
	Statuses []struct {
		Name           string `json:"name"`
		StatusCategory struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	} `json:"statuses"`
}
