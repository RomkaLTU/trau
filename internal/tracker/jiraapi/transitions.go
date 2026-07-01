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

// ErrNoTransition is returned by SetStatus when no workflow transition reaches
// the requested target status. It is deliberately not fallback-worthy: a status
// name the workflow lacks is a real error the MCP could not resolve either.
var ErrNoTransition = errors.New("jira: no matching transition")

// SetStatus moves an issue to the target status by name. Jira transitions are a
// two-step dance: GET the transitions valid from the current status, match the
// one whose destination (or transition) name equals the target, then POST it. An
// optional resolution name and comment body ride along on the same POST. Success
// is a 204 with no body.
func (c *Client) SetStatus(ctx context.Context, key, targetStatus, resolution, comment string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrNotFound
	}
	target := strings.TrimSpace(targetStatus)
	if target == "" {
		return fmt.Errorf("jira: empty target status for %s", key)
	}
	id, err := c.resolveTransition(ctx, key, target)
	if err != nil {
		return err
	}
	body, err := json.Marshal(newTransitionRequest(id, resolution, comment))
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/issue/"+url.PathEscape(key)+"/transitions", body, nil)
}

// resolveTransition fetches the transitions available from key's current status
// and returns the id of the one reaching target. It prefers a destination
// (to.name) match and falls back to the transition's own name; an unmatched
// target yields ErrNoTransition naming the statuses that were available.
func (c *Client) resolveTransition(ctx context.Context, key, target string) (string, error) {
	var resp transitionsResponse
	path := "/issue/" + url.PathEscape(key) + "/transitions"
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return "", err
	}
	nameMatch := ""
	for _, tr := range resp.Transitions {
		if strings.EqualFold(strings.TrimSpace(tr.To.Name), target) {
			return tr.ID, nil
		}
		if nameMatch == "" && strings.EqualFold(strings.TrimSpace(tr.Name), target) {
			nameMatch = tr.ID
		}
	}
	if nameMatch != "" {
		return nameMatch, nil
	}
	return "", fmt.Errorf("%w to %q for %s (available: %s)", ErrNoTransition, target, key, resp.available())
}

type transitionsResponse struct {
	Transitions []transition `json:"transitions"`
}

type transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

// available lists the destination status names the workflow offers from the
// current status, for an actionable ErrNoTransition message.
func (r transitionsResponse) available() string {
	if len(r.Transitions) == 0 {
		return "none"
	}
	names := make([]string, 0, len(r.Transitions))
	for _, tr := range r.Transitions {
		name := strings.TrimSpace(tr.To.Name)
		if name == "" {
			name = strings.TrimSpace(tr.Name)
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

type transitionRequest struct {
	Transition idRef             `json:"transition"`
	Fields     *transitionFields `json:"fields,omitempty"`
	Update     *transitionUpdate `json:"update,omitempty"`
}

type idRef struct {
	ID string `json:"id"`
}

type transitionFields struct {
	Resolution *nameRef `json:"resolution,omitempty"`
}

type nameRef struct {
	Name string `json:"name"`
}

type transitionUpdate struct {
	Comment []commentOp `json:"comment"`
}

type commentOp struct {
	Add commentAdd `json:"add"`
}

type commentAdd struct {
	Body adfDoc `json:"body"`
}

// newTransitionRequest assembles the transition POST body, attaching an optional
// resolution and an optional ADF comment when supplied.
func newTransitionRequest(id, resolution, comment string) transitionRequest {
	req := transitionRequest{Transition: idRef{ID: id}}
	if r := strings.TrimSpace(resolution); r != "" {
		req.Fields = &transitionFields{Resolution: &nameRef{Name: r}}
	}
	if c := strings.TrimSpace(comment); c != "" {
		req.Update = &transitionUpdate{Comment: []commentOp{{Add: commentAdd{Body: buildADF(c)}}}}
	}
	return req
}
