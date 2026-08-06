package bitbucketapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The pull-request states Bitbucket reports. They are spelled the way GitHub
// spells the two that matter, so the phases compare against one vocabulary
// whichever forge answered.
const (
	StateOpen       = "OPEN"
	StateMerged     = "MERGED"
	StateDeclined   = "DECLINED"
	StateSuperseded = "SUPERSEDED"
)

// PullRequest is the subset of a Bitbucket pull request trau reads back.
type PullRequest struct {
	ID    int
	URL   string
	State string
	Draft bool
}

// FindPullRequest returns the newest pull request whose source branch is branch
// and whose state is one of states, or nil when there is none. Bitbucket keeps
// every declined and superseded PR a branch ever had, so the state filter is
// what stops a re-run adopting a dead one.
func (c *Client) FindPullRequest(ctx context.Context, branch string, states ...string) (*PullRequest, error) {
	if !c.Enabled() {
		return nil, ErrNotEnabled
	}
	if branch = strings.TrimSpace(branch); branch == "" {
		return nil, ErrNotFound
	}
	q := url.Values{}
	q.Set("q", `source.branch.name="`+strings.ReplaceAll(branch, `"`, `\"`)+`"`)
	q.Set("sort", "-updated_on")
	q.Set("pagelen", "20")
	for _, s := range states {
		q.Add("state", s)
	}
	var dst struct {
		Values []pullRequestPayload `json:"values"`
	}
	if err := c.do(ctx, http.MethodGet, c.repoPath()+"/pullrequests?"+q.Encode(), nil, &dst); err != nil {
		return nil, err
	}
	for i := range dst.Values {
		pr := dst.Values[i].toPullRequest()
		if len(states) == 0 || slices.Contains(states, pr.State) {
			return &pr, nil
		}
	}
	return nil, nil
}

// PullRequest reads one pull request by its number.
func (c *Client) PullRequest(ctx context.Context, id string) (*PullRequest, error) {
	if !c.Enabled() {
		return nil, ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return nil, err
	}
	var dst pullRequestPayload
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	pr := dst.toPullRequest()
	return &pr, nil
}

// CreatePullRequest opens a pull request from head into base. A draft PR tracks
// work without asking for review, which is what exit hygiene leaves on a partial
// epic branch.
func (c *Client) CreatePullRequest(ctx context.Context, base, head, title, body string, draft bool) (*PullRequest, error) {
	if !c.Enabled() {
		return nil, ErrNotEnabled
	}
	payload, err := json.Marshal(map[string]any{
		"title":       title,
		"description": body,
		"draft":       draft,
		"source":      branchRef(head),
		"destination": branchRef(base),
	})
	if err != nil {
		return nil, err
	}
	var dst pullRequestPayload
	if err := c.do(ctx, http.MethodPost, c.repoPath()+"/pullrequests", payload, &dst); err != nil {
		return nil, err
	}
	pr := dst.toPullRequest()
	return &pr, nil
}

// UpdatePullRequest replaces a pull request's description, and takes it out of
// draft when ready is true. Bitbucket's PUT replaces the whole resource, so the
// title is read back first and re-sent unchanged rather than blanked.
func (c *Client) UpdatePullRequest(ctx context.Context, id, body string, ready bool) error {
	if !c.Enabled() {
		return ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return err
	}
	var current pullRequestPayload
	if err := c.do(ctx, http.MethodGet, path, nil, &current); err != nil {
		return err
	}
	update := map[string]any{"title": current.Title}
	if body != "" {
		update["description"] = body
	}
	if ready {
		update["draft"] = false
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, path, payload, nil)
}

// DeclinePullRequest closes a pull request without merging it — what a requeue
// does to the attempt PR a quarantined ticket left open.
func (c *Client) DeclinePullRequest(ctx context.Context, id string) error {
	if !c.Enabled() {
		return ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path+"/decline", nil, nil)
}

// Merge merges a pull request with strategy, deleting the source branch when
// deleteBranch is set.
func (c *Client) Merge(ctx context.Context, id, strategy string, deleteBranch bool) error {
	if !c.Enabled() {
		return ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"merge_strategy":      strategy,
		"close_source_branch": deleteBranch,
	})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path+"/merge", payload, nil)
}

// MergeStrategy maps trau's MERGE_METHOD onto the strategy names Bitbucket takes.
// Bitbucket has no rebase-and-merge; fast_forward is the closest thing it offers
// and is what a rebase method resolves to. An unrecognized method squashes, the
// same fallback the GitHub path applies.
func MergeStrategy(method string) string {
	switch strings.TrimSpace(strings.ToLower(method)) {
	case "merge":
		return "merge_commit"
	case "rebase":
		return "fast_forward"
	default:
		return "squash"
	}
}

// BuildStatus is one commit build status Bitbucket reports against a pull
// request's head commit — a Pipelines run, or anything else that posted through
// the build-status API.
type BuildStatus struct {
	Key   string
	Name  string
	State string
}

// Bucket rolls a Bitbucket build state up into the same pass/fail/pending/cancel
// vocabulary `gh pr checks` reports, which is what the merge gate reads.
func (s BuildStatus) Bucket() string {
	switch strings.ToUpper(strings.TrimSpace(s.State)) {
	case "SUCCESSFUL":
		return "pass"
	case "FAILED":
		return "fail"
	case "STOPPED":
		return "cancel"
	}
	return "pending"
}

// BuildStatuses lists the build statuses reported against a pull request's head
// commit. Bitbucket paginates; one page is read, which is far more statuses than
// any repository posts for a single commit.
func (c *Client) BuildStatuses(ctx context.Context, id string) ([]BuildStatus, error) {
	if !c.Enabled() {
		return nil, ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return nil, err
	}
	var dst struct {
		Values []struct {
			Key   string `json:"key"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"values"`
	}
	if err := c.do(ctx, http.MethodGet, path+"/statuses?pagelen=100", nil, &dst); err != nil {
		return nil, err
	}
	out := make([]BuildStatus, 0, len(dst.Values))
	for _, v := range dst.Values {
		name := v.Name
		if name == "" {
			name = v.Key
		}
		out = append(out, BuildStatus{Key: v.Key, Name: name, State: v.State})
	}
	return out, nil
}

// Size reports how much a pull request carries as Bitbucket sees it: the number
// of commits on it and the number of files it changes against its destination.
// Both collections are paginated and report a total in `size`; a page that omits
// it falls back to what that page held, which the merge gate treats as a floor
// rather than a fabrication.
func (c *Client) Size(ctx context.Context, id string) (commits, files int, err error) {
	if !c.Enabled() {
		return 0, 0, ErrNotEnabled
	}
	path, err := c.prPath(id)
	if err != nil {
		return 0, 0, err
	}
	if commits, err = c.pageTotal(ctx, path+"/commits?pagelen=100"); err != nil {
		return 0, 0, err
	}
	if files, err = c.pageTotal(ctx, path+"/diffstat?pagelen=100"); err != nil {
		return 0, 0, err
	}
	return commits, files, nil
}

func (c *Client) pageTotal(ctx context.Context, path string) (int, error) {
	var dst struct {
		Size   *int              `json:"size"`
		Values []json.RawMessage `json:"values"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return 0, err
	}
	if dst.Size != nil {
		return *dst.Size, nil
	}
	return len(dst.Values), nil
}

func (c *Client) prPath(id string) (string, error) {
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(id, "#")))
	if err != nil || n <= 0 {
		return "", ErrNotFound
	}
	return c.repoPath() + "/pullrequests/" + strconv.Itoa(n), nil
}

type pullRequestPayload struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	Draft bool   `json:"draft"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

func (p pullRequestPayload) toPullRequest() PullRequest {
	return PullRequest{ID: p.ID, URL: p.Links.HTML.Href, State: strings.ToUpper(p.State), Draft: p.Draft}
}

func branchRef(name string) map[string]any {
	return map[string]any{"branch": map[string]any{"name": name}}
}
