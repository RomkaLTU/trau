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

// ConnectionData resolves who the personal access token belongs to, as a stable
// id and display name. It is organization-scoped, so it answers for a repo whose
// team project is still unresolved.
func (c *Client) ConnectionData(ctx context.Context) (id, name string, err error) {
	if !c.enabled() {
		return "", "", ErrNotEnabled
	}
	var dst struct {
		AuthenticatedUser struct {
			ID                  string `json:"id"`
			ProviderDisplayName string `json:"providerDisplayName"`
		} `json:"authenticatedUser"`
	}
	if err := c.do(ctx, http.MethodGet, "/_apis/connectionData", nil, &dst); err != nil {
		return "", "", err
	}
	return dst.AuthenticatedUser.ID, dst.AuthenticatedUser.ProviderDisplayName, nil
}

// WorkItemURL is the board link to a work item. A batch read answers with the
// REST resource url, never the page a human opens, so the browser link is
// composed from the organization the client already holds.
func (c *Client) WorkItemURL(project string, id int) string {
	return c.baseURL + "/" + url.PathEscape(strings.TrimSpace(project)) + "/_workitems/edit/" + strconv.Itoa(id)
}
