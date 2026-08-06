package azureapi

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// wiqlLimit is the row ceiling a flat WIQL query answers with. Azure DevOps
// refuses a query above 20000 results outright, so a whole-project pull asks for
// exactly what the service will serve.
const wiqlLimit = 20000

// SyncIDs returns the ids of the team project's work items for a hub sync pull:
// every work item inside scope (the whole project when it is empty), narrowed
// to what changed at or after since when that is a timestamp. A flat query
// answers with ids only, so the caller follows up with WorkItems.
func (c *Client) SyncIDs(ctx context.Context, project string, scope BoardScope, since string) ([]int, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	return c.query(ctx, project, syncWIQL(project, scope, since), wiqlLimit)
}

// syncWIQL renders the query behind SyncIDs.
func syncWIQL(project string, scope BoardScope, since string) string {
	q := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = " +
		wiqlString(project) + scopeClause(scope)
	if ts, ok := wiqlTimestamp(since); ok {
		q += " AND [System.ChangedDate] >= " + wiqlString(ts)
	}
	return q + " ORDER BY [System.Id] ASC"
}

// wiqlTimestamp renders a stored sync cursor as the literal a WIQL date
// comparison accepts, truncated to whole seconds — the finest precision the
// clause parses even with timePrecision on. Truncating with an inclusive
// comparison can only widen the window, so an item is re-pulled rather than
// missed, and a cursor this cannot parse reports false to fall back on a full
// pull. The widening also absorbs the hub picking its cursor as the lexical max of
// the pulled stamps: System.ChangedDate carries variable sub-second precision, so
// that max can sit a fraction of a second behind the chronological one.
func wiqlTimestamp(since string) (string, bool) {
	if since = strings.TrimSpace(since); since == "" {
		return "", false
	}
	t, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return "", false
	}
	return t.UTC().Format("2006-01-02T15:04:05Z"), true
}

// Owner is who a personal access token belongs to: the stable identity id, the
// display name a board shows, and the account name — an email under Entra ID —
// Azure DevOps resolves an identity write against.
type Owner struct {
	ID      string
	Name    string
	Account string
}

// Assignee renders the owner as the value a System.AssignedTo write carries. The
// account name is what Azure DevOps resolves most reliably, so it wins; the display
// name stands in for a connection that reports none.
func (o Owner) Assignee() string {
	if account := strings.TrimSpace(o.Account); account != "" {
		return account
	}
	return strings.TrimSpace(o.Name)
}

// Owner resolves who the personal access token belongs to, read from
// /_apis/connectionData. It is organization-scoped, so it answers for a repo whose
// team project is still unresolved, and it needs neither the Graph host nor a scope
// beyond the ones the tracker already holds — which is what makes assigning a create
// to the token's own owner possible where identity search is not (ADR 0031 §9,
// amended by ADR 0036).
func (c *Client) Owner(ctx context.Context) (Owner, error) {
	if !c.enabled() {
		return Owner{}, ErrNotEnabled
	}
	var dst struct {
		AuthenticatedUser struct {
			ID                  string `json:"id"`
			ProviderDisplayName string `json:"providerDisplayName"`
			Properties          struct {
				Account struct {
					Value string `json:"$value"`
				} `json:"Account"`
			} `json:"properties"`
		} `json:"authenticatedUser"`
	}
	if err := c.do(ctx, http.MethodGet, "/_apis/connectionData", nil, &dst); err != nil {
		return Owner{}, err
	}
	user := dst.AuthenticatedUser
	return Owner{ID: user.ID, Name: user.ProviderDisplayName, Account: user.Properties.Account.Value}, nil
}

// WorkItemURL is the board link to a work item. A batch read answers with the
// REST resource url, never the page a human opens, so the browser link is
// composed from the organization the client already holds.
func (c *Client) WorkItemURL(project string, id int) string {
	return c.baseURL + "/" + url.PathEscape(strings.TrimSpace(project)) + "/_workitems/edit/" + strconv.Itoa(id)
}
