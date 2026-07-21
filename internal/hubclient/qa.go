package hubclient

import (
	"context"
	"net/http"
)

// QAAccount is one QA login the loop injects into the verify prompt: the label,
// username, secret, and coverage description as the hub's roster reports them.
type QAAccount struct {
	Label       string `json:"label"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
}

// QARoster is a repo's QA credentials as the loop fetches them at verify time:
// the accounts with full secret values and the free-text QA notes.
type QARoster struct {
	Accounts []QAAccount `json:"accounts"`
	Notes    string      `json:"notes"`
}

// QARoster reads the repo's QA accounts and notes from the hub with full secret
// values, for verify-prompt injection. Masking is the settings surface's job; the
// hub is localhost-only, so this path returns the credentials whole.
func (c *Client) QARoster(ctx context.Context, repo string) (QARoster, error) {
	var out QARoster
	if err := c.do(ctx, http.MethodGet, c.repoPath(repo, "qa/roster"), nil, &out); err != nil {
		return QARoster{}, err
	}
	return out, nil
}
