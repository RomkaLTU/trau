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
	return c.patch(ctx, workItemPath(project, id), ops, nil)
}

// UpdateTags adds and removes tags on a work item without disturbing the rest.
// System.Tags is a single semicolon-delimited string rather than a collection, so
// an incremental change is a read-modify-write of the whole field.
func (c *Client) UpdateTags(ctx context.Context, project string, id int, add, remove []string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	item, err := c.WorkItem(ctx, project, id)
	if err != nil {
		return err
	}
	tags := MergeTags(item.Tags, add, remove)
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
	return c.patch(ctx, workItemPath(project, id), []patchOp{historyOp(body, images)}, nil)
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

// SetDescription replaces a work item's rich-text body, rendering the markdown
// the hub holds back into the HTML the field stores.
func (c *Client) SetDescription(ctx context.Context, project string, id int, body string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	ops := []patchOp{setField("System.Description", textToHTML(body))}
	return c.patch(ctx, workItemPath(project, id), ops, nil)
}

// LinkPredecessor records that blocker must finish before blocked can start — the
// Dependency-Reverse relation the readers interpret as a blocked-by edge.
func (c *Client) LinkPredecessor(ctx context.Context, project string, blocked, blocker int) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	return c.patch(ctx, workItemPath(project, blocked), []patchOp{c.relationOp(relPredecessor, blocker)}, nil)
}

// NewWorkItem is a work item to file: its type, title, markdown body, tags, and
// the work item it hangs off (0 for top-level).
type NewWorkItem struct {
	Type        string
	Title       string
	Description string
	Tags        []string
	Parent      int
}

// CreateWorkItem files a new work item and returns its id. The type name is
// addressed as a "$Type" path segment, the shape Azure DevOps requires for
// creates.
func (c *Client) CreateWorkItem(ctx context.Context, project string, item NewWorkItem) (int, error) {
	if !c.enabled() {
		return 0, ErrNotEnabled
	}
	ops := []patchOp{setField("System.Title", item.Title)}
	if body := strings.TrimSpace(item.Description); body != "" {
		ops = append(ops, setField("System.Description", textToHTML(body)))
	}
	if len(item.Tags) > 0 {
		ops = append(ops, setField("System.Tags", JoinTags(item.Tags)))
	}
	if item.Parent > 0 {
		ops = append(ops, c.relationOp(relParent, item.Parent))
	}
	var dst struct {
		ID int `json:"id"`
	}
	path := projectPath(project, "/workitems/$"+url.PathEscape(strings.TrimSpace(item.Type)))
	if err := c.patch(ctx, path, ops, &dst); err != nil {
		return 0, err
	}
	return dst.ID, nil
}

func workItemPath(project string, id int) string {
	return projectPath(project, "/workitems/"+strconv.Itoa(id))
}
