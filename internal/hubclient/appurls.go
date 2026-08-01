package hubclient

import (
	"context"
	"net/http"
)

// AppURL is one of a repo's stored browser-verify targets as the hub reports it.
// The entry with an empty Workspace is the repo's default target; the others each
// cover the workspace they name.
type AppURL struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	Workspace string `json:"workspace"`
}

// AppURLs reads the repo's stored app URL entries from the hub. The loop calls it
// once at ticket-run start: any entry at all replaces the configured
// APP_URL/APP_URLS wholesale, and an empty answer leaves the ini values in place.
func (c *Client) AppURLs(ctx context.Context, repo string) ([]AppURL, error) {
	var out []AppURL
	if err := c.do(ctx, http.MethodGet, c.repoPath(repo, "app-urls"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
