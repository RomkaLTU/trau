package hubclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// SteerPending is the status a queued note carries until an agent consumes it or
// the ticket's run expires it — the filter the child's poll narrows with.
const SteerPending = "pending"

// SteerNote is one operator message queued against a running ticket, as the hub
// reports it. Body may span lines; DeliveredPhase carries the canonical phase
// label of the agent that consumed the note, empty until one does.
type SteerNote struct {
	ID             int64  `json:"id"`
	Ticket         string `json:"ticket"`
	Body           string `json:"body"`
	Status         string `json:"status"`
	DeliveredPhase string `json:"delivered_phase,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	DeliveredAt    string `json:"delivered_at,omitempty"`
}

// steerTicketBody carries the ticket a queue or expire call addresses, with the
// note's text on a queue.
type steerTicketBody struct {
	Ticket string `json:"ticket"`
	Body   string `json:"body,omitempty"`
}

type steerAckBody struct {
	Phase string `json:"phase"`
}

type steerNotesBody struct {
	Notes []SteerNote `json:"notes"`
}

// QueueSteer appends a steer note to a ticket's queue and returns it with the id
// that fixes its delivery order. The hub rejects an empty or whitespace-only body.
func (c *Client) QueueSteer(ctx context.Context, repo, ticket, body string) (SteerNote, error) {
	var out SteerNote
	err := c.do(ctx, http.MethodPost, c.steerPath(repo), steerTicketBody{Ticket: ticket, Body: body}, &out)
	return out, err
}

// SteerNotes returns every note the ticket has carried, oldest first — the UI
// timeline's read.
func (c *Client) SteerNotes(ctx context.Context, repo, ticket string) ([]SteerNote, error) {
	return c.steerNotes(ctx, repo, ticket, "")
}

// PendingSteerNotes returns the ticket's undelivered notes, oldest first — the
// running child's poll.
func (c *Client) PendingSteerNotes(ctx context.Context, repo, ticket string) ([]SteerNote, error) {
	return c.steerNotes(ctx, repo, ticket, SteerPending)
}

// AckSteer marks a note delivered by the agent running phase. It is idempotent;
// acking a note the settled run already expired surfaces as an error.
func (c *Client) AckSteer(ctx context.Context, repo string, id int64, phase string) error {
	path := c.steerPath(repo) + "/" + strconv.FormatInt(id, 10) + "/ack"
	return c.do(ctx, http.MethodPost, path, steerAckBody{Phase: phase}, nil)
}

// ExpireSteer sweeps the ticket's remaining pending notes once its run settles
// and returns the notes it swept, so the caller can report each. Repeating the
// sweep is harmless and yields nothing.
func (c *Client) ExpireSteer(ctx context.Context, repo, ticket string) ([]SteerNote, error) {
	var out steerNotesBody
	if err := c.do(ctx, http.MethodPost, c.steerPath(repo)+"/expire", steerTicketBody{Ticket: ticket}, &out); err != nil {
		return nil, err
	}
	return out.Notes, nil
}

func (c *Client) steerNotes(ctx context.Context, repo, ticket, status string) ([]SteerNote, error) {
	values := url.Values{"ticket": {ticket}}
	if status != "" {
		values.Set("status", status)
	}
	var out steerNotesBody
	if err := c.do(ctx, http.MethodGet, c.steerPath(repo)+"?"+values.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Notes, nil
}

func (c *Client) steerPath(repo string) string { return c.repoPath(repo, "steer") }
