package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// UpdateLabels applies incremental label add/remove ops to an issue in one
// PUT /issue call. Jira labels are freeform strings created implicitly on first
// use, so this touches only the named labels and leaves the rest intact; an
// empty op set (after trimming) is a no-op. Success is a 204.
func (c *Client) UpdateLabels(ctx context.Context, key string, add, remove []string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrNotFound
	}
	ops := labelOps(add, remove)
	if len(ops) == 0 {
		return nil
	}
	body, err := json.Marshal(issueUpdateRequest{Update: issueUpdate{Labels: ops}})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, "/issue/"+url.PathEscape(key), body, nil)
}

type issueUpdateRequest struct {
	Update issueUpdate `json:"update"`
}

type issueUpdate struct {
	Labels []labelOp `json:"labels"`
}

type labelOp struct {
	Add    string `json:"add,omitempty"`
	Remove string `json:"remove,omitempty"`
}

// labelOps builds the add/remove op list, dropping blank names.
func labelOps(add, remove []string) []labelOp {
	ops := make([]labelOp, 0, len(add)+len(remove))
	for _, l := range add {
		if l = strings.TrimSpace(l); l != "" {
			ops = append(ops, labelOp{Add: l})
		}
	}
	for _, l := range remove {
		if l = strings.TrimSpace(l); l != "" {
			ops = append(ops, labelOp{Remove: l})
		}
	}
	return ops
}

// AddComment posts a standalone comment on an issue. The v3 comment body is an
// ADF document built from the plain (possibly multi-line) text. Success is a 201.
func (c *Client) AddComment(ctx context.Context, key, text string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrNotFound
	}
	body, err := json.Marshal(commentRequest{Body: buildADF(text)})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/issue/"+url.PathEscape(key)+"/comment", body, nil)
}

// commentRequest is the standalone POST /issue/{key}/comment body — distinct from
// the transition update.comment op array in transitions.go.
type commentRequest struct {
	Body adfDoc `json:"body"`
}

// CreateIssue creates a new issue and returns its key. The issue type is resolved
// by name to its project-specific id via createmeta so the create references a
// stable id rather than a name a project may spell differently; the description
// is sent as an ADF document.
func (c *Client) CreateIssue(ctx context.Context, projectKey, issueType, summary, description string, labels []string) (string, error) {
	if !c.enabled() {
		return "", ErrNotEnabled
	}
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return "", ErrNotEnabled
	}
	typeID, err := c.resolveIssueType(ctx, projectKey, issueType)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(createIssueRequest{Fields: createFields{
		Project:     projectKeyRef{Key: projectKey},
		IssueType:   idRef{ID: typeID},
		Summary:     summary,
		Description: buildADF(description),
		Labels:      labels,
	}})
	if err != nil {
		return "", err
	}
	var resp createIssueResponse
	if err := c.do(ctx, http.MethodPost, "/issue", body, &resp); err != nil {
		return "", err
	}
	if resp.Key == "" {
		return "", errors.New("jira: create issue returned no key")
	}
	return resp.Key, nil
}

// resolveIssueType returns the id of the named issue type in a project via
// createmeta. An unmatched name is a real error the caller surfaces.
func (c *Client) resolveIssueType(ctx context.Context, projectKey, name string) (string, error) {
	var resp issueTypesResponse
	path := "/issue/createmeta/" + url.PathEscape(projectKey) + "/issuetypes"
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	for _, it := range resp.Values {
		if strings.EqualFold(strings.TrimSpace(it.Name), strings.TrimSpace(name)) {
			return it.ID, nil
		}
	}
	return "", fmt.Errorf("jira: no %q issue type in project %s", name, projectKey)
}

type issueTypesResponse struct {
	Values []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"values"`
}

type createIssueRequest struct {
	Fields createFields `json:"fields"`
}

type createFields struct {
	Project     projectKeyRef `json:"project"`
	IssueType   idRef         `json:"issuetype"`
	Summary     string        `json:"summary"`
	Description adfDoc        `json:"description"`
	Labels      []string      `json:"labels,omitempty"`
}

type projectKeyRef struct {
	Key string `json:"key"`
}

type createIssueResponse struct {
	Key string `json:"key"`
}
