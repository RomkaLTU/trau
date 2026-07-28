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

// ErrNoState is returned by SetState when the work-item type's workflow declares
// no state matching the requested target. Like Jira's missing transition, it is a
// real error rather than a fallback signal: the template simply has no such stage.
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

// historyOp appends body to a work item's discussion. Azure DevOps still gates
// its dedicated comments route behind a preview api-version, whereas writing
// System.History through the work-item PATCH is GA and rides along on whatever
// update is already in flight — one round-trip for a state change plus its note.
func historyOp(body string) patchOp {
	return setField("System.History", textToHTML(body))
}

// SetState moves a work item to the state matching target, optionally appending a
// comment in the same request. Azure DevOps has no transition graph to walk:
// System.State is written directly, so the work is resolving the loop's target
// name against whatever states the project's process template declares.
func (c *Client) SetState(ctx context.Context, project string, id int, target, comment string) error {
	if !c.enabled() {
		return ErrNotEnabled
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("azure: empty target state for %d", id)
	}
	item, err := c.WorkItem(ctx, project, id)
	if err != nil {
		return err
	}
	states, err := c.States(ctx, project, item.Type)
	if err != nil {
		return err
	}
	state, err := resolveState(states, target)
	if err != nil {
		return fmt.Errorf("%w on %s #%d", err, item.Type, id)
	}
	ops := []patchOp{setField("System.State", state)}
	if body := strings.TrimSpace(comment); body != "" {
		ops = append(ops, historyOp(body))
	}
	return c.patch(ctx, workItemPath(project, id), ops, nil)
}

// resolveState picks the state name to write for target. An exact
// (case-insensitive) name match wins; failing that the target's category selects
// the first state the type declares in that category.
func resolveState(states []State, target string) (string, error) {
	for _, s := range states {
		if strings.EqualFold(strings.TrimSpace(s.Name), target) {
			return s.Name, nil
		}
	}
	for _, want := range categoryFallbacks(TargetCategory(target)) {
		for _, s := range states {
			if strings.EqualFold(strings.TrimSpace(s.Category), string(want)) {
				return s.Name, nil
			}
		}
	}
	return "", fmt.Errorf("%w %q (available: %s)", ErrNoState, target, stateNames(states))
}

// categoryFallbacks orders the categories a target may settle for when the
// project's template declares no state in its first choice. Scrum has no Resolved
// category, so an "In Review" target lands on the in-progress state instead of
// failing — the work is still live either way.
func categoryFallbacks(want StateCategory) []StateCategory {
	switch want {
	case CategoryResolved:
		return []StateCategory{CategoryResolved, CategoryInProgress}
	case CategoryInProgress:
		return []StateCategory{CategoryInProgress, CategoryResolved}
	case CategoryRemoved:
		return []StateCategory{CategoryRemoved, CategoryCompleted}
	case CategoryUnknown:
		return nil
	default:
		return []StateCategory{want}
	}
}

func stateNames(states []State) string {
	if len(states) == 0 {
		return "none"
	}
	names := make([]string, len(states))
	for i, s := range states {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
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
	if !c.enabled() {
		return ErrNotEnabled
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	return c.patch(ctx, workItemPath(project, id), []patchOp{historyOp(body)}, nil)
}

// Comments reads a work item's discussion, newest last, for the build prompt's
// ticket context.
func (c *Client) Comments(ctx context.Context, project string, id int) ([]Comment, error) {
	if !c.enabled() {
		return nil, ErrNotEnabled
	}
	var dst struct {
		Comments []struct {
			Text      string `json:"text"`
			CreatedBy struct {
				DisplayName string `json:"displayName"`
			} `json:"createdBy"`
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
		out = append(out, Comment{Author: raw.CreatedBy.DisplayName, Body: body})
	}
	return out, nil
}

// Comment is one entry in a work item's discussion.
type Comment struct {
	Author string
	Body   string
}

// CreateWorkItem files a new work item of itemType and returns its id. The type
// name is addressed as a "$Type" path segment, the shape Azure DevOps requires
// for creates.
func (c *Client) CreateWorkItem(ctx context.Context, project, itemType, title, description string, tags []string) (int, error) {
	if !c.enabled() {
		return 0, ErrNotEnabled
	}
	ops := []patchOp{setField("System.Title", title)}
	if body := strings.TrimSpace(description); body != "" {
		ops = append(ops, setField("System.Description", textToHTML(body)))
	}
	if len(tags) > 0 {
		ops = append(ops, setField("System.Tags", JoinTags(tags)))
	}
	var dst struct {
		ID int `json:"id"`
	}
	path := projectPath(project, "/workitems/$"+url.PathEscape(strings.TrimSpace(itemType)))
	if err := c.patch(ctx, path, ops, &dst); err != nil {
		return 0, err
	}
	return dst.ID, nil
}

func workItemPath(project string, id int) string {
	return projectPath(project, "/workitems/"+strconv.Itoa(id))
}
