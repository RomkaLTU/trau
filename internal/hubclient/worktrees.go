package hubclient

import (
	"context"
	"net/http"
)

// The worktree standings the hub records, mirrored here so a loop naming one does
// not hard-code a string the store owns.
const (
	WorktreeActive  = "active"
	WorktreeSettled = "settled"
)

// worktreeBody is what a loop reports about a tree it created, adopted, or settled.
type worktreeBody struct {
	Ticket string `json:"ticket"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	State  string `json:"state,omitempty"`
}

// ReportWorktree tells the hub a run created or adopted a tree for a ticket. It is
// keyed by ticket, so a resume that adopts the tree refreshes the row it already
// has rather than filing a second one.
func (c *Client) ReportWorktree(ctx context.Context, repo, ticket, path, branch string) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "worktrees"),
		worktreeBody{Ticket: ticket, Path: path, Branch: branch, State: WorktreeActive}, nil)
}

// SettleWorktree tells the hub a ticket's tree is finished with. The hub removes
// whatever the local removal left behind and marks the row settled, so a CLI
// reset/requeue/purge and the hub's own settle leave the same record.
func (c *Client) SettleWorktree(ctx context.Context, repo, ticket, path string) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "worktrees"),
		worktreeBody{Ticket: ticket, Path: path, State: WorktreeSettled}, nil)
}
