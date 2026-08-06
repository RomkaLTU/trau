package azureapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// ErrNoState is returned when the work-item type's workflow declares no state
// matching the lifecycle stage a caller asked for. Like Jira's missing
// transition, it is a real error rather than a fallback signal: the template
// simply has no such stage.
var ErrNoState = errors.New("azure: no matching state")

// fieldPath is the JSON-Patch path prefix every work-item field update writes to.
const fieldPath = "/fields/"

// TrauTag marks a work item trau has written to. Every write that changes an item
// carries it, not only a create, so a board reader can tell the loop's own edits
// from a teammate's (ADR 0036).
const TrauTag = "trau"

// rankGap is how far above the current top of a board column a create ranks
// itself. Azure DevOps spaces Stack Rank widely on purpose — a board drag rewrites
// one item's rank rather than renumbering the column — so a create takes a whole
// gap rather than splitting the distance to the item below it.
const rankGap = 1000

// rankProbe is how many rows of a Stack-Rank-ordered column RankTop reads to find
// the rank it must beat. One row would be enough if every work item carried a rank,
// but a column may hold items that carry none, and which end of the order those
// land on is the service's choice — so the probe reads past them instead of
// assuming. The first ranked row in the window is the column's lowest rank either
// way.
const rankProbe = 20

// patchOp is one operation in the JSON-Patch document Azure DevOps requires for
// every work-item create and update.
type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func setField(field string, value any) patchOp {
	return patchOp{Op: "add", Path: fieldPath + field, Value: value}
}

// markdownFormat is the format a multiline field carries once it holds markdown
// rather than HTML. A read matches it to know which of the two it is looking at.
const markdownFormat = "Markdown"

// markdownField writes body into a rich-text field as real markdown. Azure DevOps
// records a multiline field's format outside /fields, on its own patch path, and
// the conversion away from HTML is one-way per Microsoft's documentation — so the
// format op rides along on every write of a field trau owns rather than being set
// once and assumed (ADR 0036).
func markdownField(field, body string) []patchOp {
	return []patchOp{
		setField(field, body),
		{Op: "add", Path: "/multilineFieldsFormat/" + field, Value: markdownFormat},
	}
}

// relationOp links a work item to another by the target's REST resource URL, the
// only form the API accepts for a relation. The URL is organization-scoped, so it
// holds whichever team project either side lives in.
func (c *Client) relationOp(rel string, target int) patchOp {
	return patchOp{
		Op:   "add",
		Path: "/relations/-",
		Value: map[string]string{
			"rel": rel,
			"url": c.baseURL + "/_apis/wit/workItems/" + strconv.Itoa(target),
		},
	}
}

// historyOp appends body to a work item's discussion, with any uploaded images
// embedded under it. Azure DevOps still gates its dedicated comments route behind
// a preview api-version, whereas writing System.History through the work-item
// PATCH is GA and rides along on whatever update is already in flight — one
// round-trip for a state change plus its note.
func historyOp(body string, images []Attachment) patchOp {
	return setField("System.History", textToHTML(body)+imagesHTML(images))
}

// attachmentOp lists an uploaded file under the work item's Attachments. Unlike
// relationOp's work-item links the target is the file's own URL, and its caption
// rides along as the relation's comment.
func attachmentOp(file Attachment) patchOp {
	return patchOp{
		Op:   "add",
		Path: "/relations/-",
		Value: map[string]any{
			"rel":        relAttachedFile,
			"url":        file.URL,
			"attributes": map[string]string{"comment": file.Caption},
		},
	}
}

// update applies ops to a work item with the trau mark folded into the same
// request. System.Tags is one flat string rather than a collection, so stamping the
// mark costs a read of the item's current tags before the write — the price of the
// mark being on every change trau makes rather than only on a create.
func (c *Client) update(ctx context.Context, project string, id int, ops []patchOp) error {
	item, err := c.WorkItem(ctx, project, id)
	if err != nil {
		return err
	}
	if tags := MergeTags(item.Tags, []string{TrauTag}, nil); !slices.Equal(tags, item.Tags) {
		ops = append(ops, setField("System.Tags", JoinTags(tags)))
	}
	return c.patch(ctx, workItemPath(project, id), ops, nil)
}

// SetState writes state onto a work item, optionally appending a comment in the
// same request. Azure DevOps has no transition graph to walk — System.State is
// written directly — so state must already be one the project's process template
// declares (see States).
func (c *Client) SetState(ctx context.Context, project string, id int, state, comment string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	if strings.TrimSpace(state) == "" {
		return fmt.Errorf("azure: empty target state for %d", id)
	}
	ops := []patchOp{setField("System.State", state)}
	if body := strings.TrimSpace(comment); body != "" {
		ops = append(ops, historyOp(body, nil))
	}
	return c.update(ctx, project, id, ops)
}

// UpdateTags adds and removes tags on a work item without disturbing the rest,
// with the trau mark added alongside. System.Tags is a single semicolon-delimited
// string rather than a collection, so an incremental change is a read-modify-write
// of the whole field.
func (c *Client) UpdateTags(ctx context.Context, project string, id int, add, remove []string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	item, err := c.WorkItem(ctx, project, id)
	if err != nil {
		return err
	}
	tags := MergeTags(item.Tags, slices.Concat(add, []string{TrauTag}), remove)
	if slices.Equal(tags, item.Tags) {
		return nil
	}
	return c.patch(ctx, workItemPath(project, id), []patchOp{setField("System.Tags", JoinTags(tags))}, nil)
}

// MergeTags applies add and remove to an existing tag list, matching
// case-insensitively but preserving the casing already on the work item. Order is
// stable so an unchanged set compares equal and skips the write.
func MergeTags(current, add, remove []string) []string {
	drop := make(map[string]bool, len(remove))
	for _, tag := range remove {
		if t := strings.TrimSpace(tag); t != "" {
			drop[strings.ToLower(t)] = true
		}
	}
	out := make([]string, 0, len(current)+len(add))
	have := make(map[string]bool, len(current)+len(add))
	for _, tag := range current {
		key := strings.ToLower(tag)
		if drop[key] || have[key] {
			continue
		}
		have[key] = true
		out = append(out, tag)
	}
	for _, tag := range add {
		tag = strings.TrimSpace(tag)
		key := strings.ToLower(tag)
		if tag == "" || drop[key] || have[key] {
			continue
		}
		have[key] = true
		out = append(out, tag)
	}
	return out
}

// AddComment appends a comment to a work item's discussion.
func (c *Client) AddComment(ctx context.Context, project string, id int, body string) error {
	return c.AddCommentWithImages(ctx, project, id, body, nil)
}

// AddCommentWithImages is AddComment with uploaded images embedded under the
// body, each as an <img> pointing at the URL its upload returned.
func (c *Client) AddCommentWithImages(ctx context.Context, project string, id int, body string, images []Attachment) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	return c.update(ctx, project, id, []patchOp{historyOp(body, images)})
}

// Attachment is one uploaded file: the URL the upload returned, which both the
// comment's <img> and the work item's attachment list reference, and its caption.
type Attachment struct {
	URL     string
	Caption string
}

// UploadAttachment stores data in the attachment store and returns the URL a work
// item references it by. The endpoint takes the raw bytes — no multipart envelope
// — and names the file through the query string.
func (c *Client) UploadAttachment(ctx context.Context, project, filename string, data []byte) (string, error) {
	if !c.enabled() {
		return "", ErrNotEnabled
	}
	var dst struct {
		URL string `json:"url"`
	}
	path := projectPath(project, "/attachments?fileName="+url.QueryEscape(filename))
	if err := c.send(ctx, http.MethodPost, path, data, "application/octet-stream", &dst); err != nil {
		return "", err
	}
	if dst.URL == "" {
		return "", fmt.Errorf("azure: upload of %q returned no attachment URL", filename)
	}
	return dst.URL, nil
}

// AttachFiles lists uploaded files under a work item's Attachments, so they are
// reachable whatever the discussion renderer makes of an inline <img>.
func (c *Client) AttachFiles(ctx context.Context, project string, id int, files []Attachment) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	if len(files) == 0 {
		return nil
	}
	ops := make([]patchOp, 0, len(files))
	for _, f := range files {
		ops = append(ops, attachmentOp(f))
	}
	return c.patch(ctx, workItemPath(project, id), ops, nil)
}

// Comments reads a work item's discussion, newest last, for the build prompt's
// ticket context.
func (c *Client) Comments(ctx context.Context, project string, id int) ([]Comment, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Comments []struct {
			ID        int    `json:"id"`
			Text      string `json:"text"`
			CreatedBy struct {
				DisplayName string `json:"displayName"`
			} `json:"createdBy"`
			CreatedDate  string `json:"createdDate"`
			ModifiedDate string `json:"modifiedDate"`
		} `json:"comments"`
	}
	path := projectPath(project, "/workitems/"+strconv.Itoa(id)+"/comments") +
		"?$top=" + strconv.Itoa(batchLimit) + "&api-version=" + commentsAPIVersion
	if err := c.do(ctx, http.MethodGet, path, nil, &dst); err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(dst.Comments))
	for _, raw := range dst.Comments {
		body := htmlToMarkdown(raw.Text)
		if body == "" {
			continue
		}
		out = append(out, Comment{
			ID:        raw.ID,
			Author:    raw.CreatedBy.DisplayName,
			Body:      body,
			CreatedAt: raw.CreatedDate,
			UpdatedAt: raw.ModifiedDate,
		})
	}
	return out, nil
}

// Comment is one entry in a work item's discussion. ID is the comment's own
// identifier, which a sync stores so a re-pull updates the entry rather than
// filing it again.
type Comment struct {
	ID        int
	Author    string
	Body      string
	CreatedAt string
	UpdatedAt string
}

// Body is the markdown a work item's rich-text fields hold, split the way Azure
// DevOps stores it. HasAcceptance separates a work-item type that carries an
// acceptance-criteria field and has none written — which a write clears — from one
// with no such field to write at all.
type Body struct {
	Description   string
	Acceptance    string
	HasAcceptance bool
}

// SplitBody splits the single markdown body trau carries into the fields it lands
// in, cutting on the *last* top-level acceptance-criteria heading: that is the one
// the reader appends the criteria field under, so the cut is the exact inverse of
// the reader's emission even when the description carries a heading of its own.
// hasAcceptance reports whether the target work-item type carries the field: a Task
// carries none, so its whole body — the heading included — stays in the description.
func SplitBody(markdown string, hasAcceptance bool) Body {
	body := strings.TrimSpace(strings.ReplaceAll(markdown, "\r\n", "\n"))
	if !hasAcceptance {
		return Body{Description: body}
	}
	out := Body{Description: body, HasAcceptance: true}
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(lines[i]), acceptanceHeading) {
			continue
		}
		out.Description = strings.TrimSpace(strings.Join(lines[:i], "\n"))
		out.Acceptance = strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		break
	}
	return out
}

// SetBody replaces a work item's description and acceptance criteria, both as real
// markdown rather than the HTML the fields held before trau first wrote them.
func (c *Client) SetBody(ctx context.Context, project string, id int, body Body) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	ops := markdownField(fieldDescription, body.Description)
	if body.HasAcceptance {
		ops = append(ops, markdownField(fieldAcceptance, body.Acceptance)...)
	}
	return c.update(ctx, project, id, ops)
}

// LinkPredecessor records that blocker must finish before blocked can start — the
// Dependency-Reverse relation the readers interpret as a blocked-by edge.
func (c *Client) LinkPredecessor(ctx context.Context, project string, blocked, blocker int) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	return c.patch(ctx, workItemPath(project, blocked), []patchOp{c.relationOp(relPredecessor, blocker)}, nil)
}

// NewWorkItem is a work item to file: its type, title, markdown body, tags, the
// work item it hangs off (0 for top-level), and the identity it is assigned to
// ("" leaves it unassigned).
type NewWorkItem struct {
	Type     string
	Title    string
	Body     Body
	Tags     []string
	Parent   int
	Assignee string
}

// Created is a filed work item: its number and the board column it landed in,
// which is the column RankTop measures a top-of-board position against.
type Created struct {
	ID          int
	BoardColumn string
}

// CreateWorkItem files a new work item, marked with the trau tag. The type name is
// addressed as a "$Type" path segment, the shape Azure DevOps requires for
// creates.
func (c *Client) CreateWorkItem(ctx context.Context, project string, item NewWorkItem) (Created, error) {
	if !c.enabled() {
		return Created{}, ErrNotEnabled
	}
	ops := []patchOp{setField("System.Title", item.Title)}
	if body := strings.TrimSpace(item.Body.Description); body != "" {
		ops = append(ops, markdownField(fieldDescription, body)...)
	}
	if criteria := strings.TrimSpace(item.Body.Acceptance); criteria != "" {
		ops = append(ops, markdownField(fieldAcceptance, criteria)...)
	}
	ops = append(ops, setField("System.Tags", JoinTags(MergeTags(item.Tags, []string{TrauTag}, nil))))
	if assignee := strings.TrimSpace(item.Assignee); assignee != "" {
		ops = append(ops, setField("System.AssignedTo", assignee))
	}
	if item.Parent > 0 {
		ops = append(ops, c.relationOp(relParent, item.Parent))
	}
	var dst workItemResponse
	path := projectPath(project, "/workitems/$"+url.PathEscape(strings.TrimSpace(item.Type)))
	if err := c.patch(ctx, path, ops, &dst); err != nil {
		return Created{}, err
	}
	return Created{ID: dst.ID, BoardColumn: dst.Fields.BoardColumn}, nil
}

// RankTop moves a work item to the top of the board column it sits in — where a
// create belongs, since that is the end of the column the team reads first (ADR
// 0036). A column whose items carry no Stack Rank leaves the rank alone: there is no
// order for the item to be at the top of.
func (c *Client) RankTop(ctx context.Context, project, column string, id int) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	if strings.TrimSpace(column) == "" {
		return nil
	}
	ids, err := c.query(ctx, project, topOfColumnWIQL(project, column), rankProbe)
	if err != nil {
		return err
	}
	ids = slices.DeleteFunc(ids, func(other int) bool { return other == id })
	if len(ids) == 0 {
		return nil
	}
	items, err := c.WorkItems(ctx, project, ids)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.StackRank == nil {
			continue
		}
		return c.patch(ctx, workItemPath(project, id),
			[]patchOp{setField("Microsoft.VSTS.Common.StackRank", *item.StackRank-rankGap)}, nil)
	}
	return nil
}

// topOfColumnWIQL orders a board column by the rank the team dragged it into, so
// the head of the answer is the position a create has to beat.
func topOfColumnWIQL(project, column string) string {
	return "SELECT [System.Id] FROM WorkItems" +
		" WHERE [System.TeamProject] = " + wiqlString(project) +
		" AND [System.BoardColumn] = " + wiqlString(column) +
		" ORDER BY [Microsoft.VSTS.Common.StackRank] ASC"
}

func workItemPath(project string, id int) string {
	return projectPath(project, "/workitems/"+strconv.Itoa(id))
}
