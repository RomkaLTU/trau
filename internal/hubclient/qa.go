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

// QASourceAgent is the provenance the hub stamps on a credential the verifier
// discovered inside the repo under test, as opposed to one a person entered in
// settings.
const QASourceAgent = "agent"

// QAAccountInput is a QA account the loop asks the hub to store.
type QAAccountInput struct {
	Label       string `json:"label"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
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

// CreateQAAccount stores a new QA account for the repo, so a credential captured
// on this run prefills the next run's roster. A label already taken in the repo
// is rejected by the hub and surfaces as an error here.
func (c *Client) CreateQAAccount(ctx context.Context, repo string, in QAAccountInput) error {
	return c.do(ctx, http.MethodPost, c.repoPath(repo, "qa/accounts"), in, nil)
}
