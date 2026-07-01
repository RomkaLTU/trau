package jiraapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// projectSearchPath enumerates the projects a token can see. Unlike /search/jql
// (nextPageToken) this older endpoint pages with startAt/maxResults.
const projectSearchPath = "/project/search"

const (
	// projectPageSize caps projects per page.
	projectPageSize = 50
	// projectMaxPages bounds the pagination loop so a huge or misbehaving result
	// set can't spin indefinitely.
	projectMaxPages = 100
)

// ListProjects returns every project the token can see, paging /project/search
// by startAt until the last page. An empty token yields ErrNotEnabled so the
// caller falls back to the MCP.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var all []Project
	startAt := 0
	for page := 0; page < projectMaxPages; page++ {
		q := url.Values{}
		q.Set("startAt", strconv.Itoa(startAt))
		q.Set("maxResults", strconv.Itoa(projectPageSize))
		var resp projectSearchResponse
		if err := c.do(ctx, http.MethodGet, projectSearchPath+"?"+q.Encode(), nil, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Values {
			all = append(all, Project(p))
		}
		if resp.IsLast || len(resp.Values) == 0 || startAt+len(resp.Values) >= resp.Total {
			break
		}
		startAt += len(resp.Values)
	}
	return all, nil
}

type projectSearchResponse struct {
	Values     []projectValue `json:"values"`
	StartAt    int            `json:"startAt"`
	MaxResults int            `json:"maxResults"`
	Total      int            `json:"total"`
	IsLast     bool           `json:"isLast"`
}

type projectValue struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	ID   string `json:"id"`
}
