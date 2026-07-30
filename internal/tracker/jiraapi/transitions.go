package jiraapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ErrNoTransition is returned when no workflow transition reaches the lifecycle
// stage a caller asked for. It is deliberately not fallback-worthy: a workflow
// that offers no such destination is a real error the MCP could not resolve
// either.
var ErrNoTransition = errors.New("jira: no matching transition")

// Transition is one destination the workflow offers from an issue's current
// status: the transition to POST, and the status it lands on.
type Transition struct {
	ID     string
	Name   string
	Status Status
}

// Transitions returns the workflow transitions valid from key's current status —
// the first half of Jira's two-step transition dance. Each carries the
// destination's statusCategory so a caller can resolve a stage without knowing
// what this workflow names its statuses.
func (c *Client) Transitions(ctx context.Context, key string) ([]Transition, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrNotFound
	}
	var resp transitionsResponse
	if err := c.do(ctx, http.MethodGet, transitionsPath(key), nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(resp.Transitions))
	for _, tr := range resp.Transitions {
		out = append(out, Transition{
			ID:   tr.ID,
			Name: strings.TrimSpace(tr.Name),
			Status: Status{
				Name:     strings.TrimSpace(tr.To.Name),
				Category: strings.TrimSpace(tr.To.StatusCategory.Key),
			},
		})
	}
	return out, nil
}

// ApplyTransition executes transition id on key — the second half of the dance.
// An optional resolution name and comment body ride along on the same POST.
// Success is a 204 with no body.
func (c *Client) ApplyTransition(ctx context.Context, key, id, resolution, comment string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrNotFound
	}
	body, err := json.Marshal(newTransitionRequest(id, resolution, comment))
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, transitionsPath(key), body, nil)
}

func transitionsPath(key string) string {
	return "/issue/" + url.PathEscape(key) + "/transitions"
}

// Destination is the status this transition lands on, falling back to the
// transition's own name for the rare workflow that reports no destination.
func (t Transition) Destination() string {
	if t.Status.Name != "" {
		return t.Status.Name
	}
	return t.Name
}

type transitionsResponse struct {
	Transitions []transition `json:"transitions"`
}

type transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name           string `json:"name"`
		StatusCategory struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	} `json:"to"`
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
