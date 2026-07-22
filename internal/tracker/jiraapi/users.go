package jiraapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// assignableUserSearchPath lists the users a project's issues can be assigned to.
const assignableUserSearchPath = "/user/assignable/search"

// assignableUsersPageSize caps the assignable-user lookup at one page; the picker
// narrows with a query rather than paging a whole site.
const assignableUsersPageSize = 50

// User is a Jira user an issue can be assigned to. ID is the canonical accountId;
// users are never matched by email, which Atlassian privacy settings can hide.
type User struct {
	ID   string
	Name string
}

// AssignableUsers returns the users assignable in a project, narrowed to those
// matching query when it is non-empty. An empty project key yields ErrNotEnabled —
// Jira scopes assignability to a project, so there is nothing to ask without one.
func (c *Client) AssignableUsers(ctx context.Context, projectKey, query string) ([]User, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return nil, ErrNotEnabled
	}
	q := url.Values{}
	q.Set("project", projectKey)
	q.Set("maxResults", strconv.Itoa(assignableUsersPageSize))
	if query = strings.TrimSpace(query); query != "" {
		q.Set("query", query)
	}
	var resp []assignableUser
	if err := c.do(ctx, http.MethodGet, assignableUserSearchPath+"?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(resp))
	for _, u := range resp {
		out = append(out, User{ID: u.AccountID, Name: u.DisplayName})
	}
	return out, nil
}

type assignableUser struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}
