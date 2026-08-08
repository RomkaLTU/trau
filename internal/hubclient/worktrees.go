package hubclient

import (
	"context"
	"net/http"
	"time"
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

// worktreeAppHTTP carries the ensure-app call. The hub holds that request open
// while it waits for the app's port to accept, so the standard budget would cut
// the answer short of the hub's own readiness window.
var worktreeAppHTTP = &http.Client{Timeout: 2 * time.Minute}

// WorktreeApp is what the hub reports about the app it serves out of a tree.
// Serving is false when the repo has no APP_START_CMD at all — the caller keeps
// its configured browser-verify target — while a true Serving with an empty URL
// means the app was meant to run and did not come up.
type WorktreeApp struct {
	Ticket  string `json:"ticket"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Output  string `json:"output"`
	Serving bool   `json:"serving"`
}

// EnsureWorktreeApp asks the hub to have the ticket's worktree app running and
// hands back where it is. It is the run child's "give me a verify URL": the hub
// starts the app at first need and waits for its port to accept, so the answer is
// a URL that is live or a failure that says why.
func (c *Client) EnsureWorktreeApp(ctx context.Context, repo, ticket string) (WorktreeApp, error) {
	var out WorktreeApp
	if err := c.doWith(ctx, worktreeAppHTTP, http.MethodPost, c.repoPath(repo, "worktrees/app"),
		worktreeAppBody{Ticket: ticket}, &out); err != nil {
		return WorktreeApp{}, err
	}
	return out, nil
}

// worktreeAppBody names the tree whose app to bring up, by the key the tree was
// reported under — an epic's own id for the tree its children share.
type worktreeAppBody struct {
	Ticket string `json:"ticket"`
}
