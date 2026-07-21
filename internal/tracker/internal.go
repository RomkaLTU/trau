package tracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// sourceInternal is the issue-store source binding for internally-created issues
// (ADR 0007). It matches hubstore.SourceInternal but is duplicated to keep the
// tracker package off the store's database dependencies.
const sourceInternal = "internal"

// hubAPI is the slice of the hub's internal-issue API the internal provider
// drives. *hubclient.Client satisfies it; tests supply a fake.
type hubAPI interface {
	InternalIssue(ctx context.Context, repo, id string) (hubclient.Issue, error)
	Backlog(ctx context.Context, repo string, q hubclient.BacklogQuery) ([]hubclient.BacklogItem, error)
	CreateInternalIssue(ctx context.Context, repo string, d hubclient.InternalDraft) (hubclient.Issue, error)
	TransitionInternalIssue(ctx context.Context, repo, id string, t hubclient.Transition) (hubclient.Issue, error)
}

// Internal is the tracker provider for repos with no external tracker: it drives
// internal issues (ADR 0007) entirely through the serve hub's HTTP API, so the
// loop reads and writes issues without ever opening the hub database. Pick honors
// the ready label and workflow state against the issue store; status, labels, and
// comments write back through the same API.
type Internal struct {
	Hub             hubAPI
	Repo            string
	ReadyLabel      string
	QuarantineLabel string
}

// Pick returns the lowest-numbered ready, unstarted, unblocked leaf internal issue
// in scope, or "" when none is eligible.
func (in *Internal) Pick(ctx context.Context, scope Scope) (string, error) {
	candidates, err := in.eligible(ctx, scope)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	return candidates[0].ID, nil
}

// ListEligible enumerates the internal issues Pick would consider, ordered the
// same way — the source for --list-eligible and the hub's eligible board.
func (in *Internal) ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error) {
	candidates, err := in.eligible(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]ListedTicket, 0, len(candidates))
	for _, it := range candidates {
		out = append(out, ListedTicket{
			ID:          it.ID,
			Title:       it.Title,
			Labels:      it.Labels,
			Parent:      it.Parent,
			HasChildren: it.HasChildren,
		})
	}
	return out, nil
}

// eligible reads the repo's ready-labelled internal backlog and narrows it to the
// runnable candidates: an unstarted, unblocked leaf issue, restricted to a scoped
// parent's children when the scope carries one, ordered by ascending issue number.
func (in *Internal) eligible(ctx context.Context, scope Scope) ([]hubclient.BacklogItem, error) {
	items, err := in.Hub.Backlog(ctx, in.Repo, hubclient.BacklogQuery{Source: sourceInternal, Label: in.ReadyLabel})
	if err != nil {
		return nil, err
	}
	out := make([]hubclient.BacklogItem, 0, len(items))
	for _, it := range items {
		if it.Source != sourceInternal || it.HasChildren || it.Blocked {
			continue
		}
		if it.Group != "backlog" && it.Group != "unstarted" {
			continue
		}
		if scope.Parent != "" && it.Parent != scope.Parent {
			continue
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := issueNumber(out[i].ID), issueNumber(out[j].ID); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// SubIssues returns the internal children of id, ordered by issue number.
func (in *Internal) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	items, err := in.Hub.Backlog(ctx, in.Repo, hubclient.BacklogQuery{Source: sourceInternal})
	if err != nil {
		return nil, err
	}
	out := make([]SubIssue, 0)
	for _, it := range items {
		if it.Parent != id {
			continue
		}
		out = append(out, SubIssue{
			ID:          it.ID,
			Title:       it.Title,
			Done:        it.Group == "done" || it.Group == "canceled",
			HasChildren: it.HasChildren,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := issueNumber(out[i].ID), issueNumber(out[j].ID); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Title returns the title of internal issue id, best-effort — an error yields "".
func (in *Internal) Title(ctx context.Context, id string) (string, error) {
	iss, err := in.Hub.InternalIssue(ctx, in.Repo, id)
	if err != nil {
		return "", err
	}
	return iss.Title, nil
}

// IssueDetail returns the title and description of internal issue id for
// build-prompt context.
func (in *Internal) IssueDetail(ctx context.Context, id string) (IssueDetail, error) {
	iss, err := in.Hub.InternalIssue(ctx, in.Repo, id)
	if err != nil {
		return IssueDetail{}, err
	}
	return IssueDetail{Title: iss.Title, Description: iss.Description}, nil
}

// IssueStatus reports the normalized lifecycle status of internal issue id. A
// missing issue yields StatusUnknown so a --status reconcile leaves the checkpoint
// intact rather than clearing live work.
func (in *Internal) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	iss, err := in.Hub.InternalIssue(ctx, in.Repo, id)
	if err != nil {
		if errors.Is(err, hubclient.ErrNotFound) {
			return StatusUnknown, nil
		}
		return StatusUnknown, err
	}
	switch iss.State {
	case "done":
		return StatusDone, nil
	case "canceled":
		return StatusCanceled, nil
	default:
		return StatusOpen, nil
	}
}

// ParentIssue reports the epic an internal issue belongs to, or "" when it is
// top-level. A missing issue reads as no parent so epic stacking never forces an
// unwanted branch.
func (in *Internal) ParentIssue(ctx context.Context, id string) (string, error) {
	iss, err := in.Hub.InternalIssue(ctx, in.Repo, id)
	if err != nil {
		if errors.Is(err, hubclient.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return iss.Parent, nil
}

// SetStatus moves an internal issue to the workflow state matching status and
// appends extra as a comment. An unmapped status leaves the state unchanged.
func (in *Internal) SetStatus(ctx context.Context, id, status, extra string) error {
	_, err := in.Hub.TransitionInternalIssue(ctx, in.Repo, id, hubclient.Transition{
		State:   internalStateFor(status),
		Comment: strings.TrimSpace(extra),
	})
	return err
}

// Reset returns an internal issue to an unstarted, ready state so the picker
// re-selects it: it restores the ready label, drops the quarantine label, moves the
// issue to unstarted, and comments.
func (in *Internal) Reset(ctx context.Context, id string) error {
	_, err := in.Hub.TransitionInternalIssue(ctx, in.Repo, id, hubclient.Transition{
		State:        "unstarted",
		AddLabels:    labelsOf(in.ReadyLabel),
		RemoveLabels: labelsOf(in.QuarantineLabel),
		Comment:      fmt.Sprintf("Trau loop reset %s to start fresh.", id),
	})
	return err
}

// Quarantine marks an internal issue unrecoverable: it drops the ready label, adds
// the quarantine (needs-human) label, and leaves a comment pointing at the run
// artifacts. The workflow state is left as-is, mirroring the external providers.
func (in *Internal) Quarantine(ctx context.Context, id, reason string) error {
	_, err := in.Hub.TransitionInternalIssue(ctx, in.Repo, id, hubclient.Transition{
		AddLabels:    labelsOf(in.QuarantineLabel),
		RemoveLabels: labelsOf(in.ReadyLabel),
		Comment:      fmt.Sprintf("Trau loop stopped: %s (see this ticket's run in the trau web UI).", reason),
	})
	return err
}

// FileBug files a new internal issue as the HITL blocker when the verify loop's
// repair and bugfix passes could not resolve a QA failure (ADR 0007: trau-filed
// bugs are internal, never external). It carries the QA verdict and needs-human
// labels, and is not marked ready so the loop never re-picks it.
func (in *Internal) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
	iss, err := in.Hub.CreateInternalIssue(ctx, in.Repo, hubclient.InternalDraft{
		Title:       fmt.Sprintf("QA blocked: %s needs human attention", id),
		Description: fileBugBody(id, verdictPath),
		Labels:      []string{"HITL", "Bug"},
	})
	if err != nil {
		return "", err
	}
	return iss.ID, nil
}

// AddLabel adds one label to an internal issue, keeping its others.
func (in *Internal) AddLabel(ctx context.Context, id, label string) error {
	if strings.TrimSpace(label) == "" {
		return nil
	}
	_, err := in.Hub.TransitionInternalIssue(ctx, in.Repo, id, hubclient.Transition{AddLabels: []string{label}})
	return err
}

// EnsureLabels is a no-op for internal issues: labels are free-form strings on the
// issue row, so there is nothing to provision.
func (in *Internal) EnsureLabels(context.Context) error { return nil }

// internalStateFor maps a pipeline display status onto an internal workflow state
// group, or "" to leave the state unchanged for an unrecognized status.
func internalStateFor(status string) string {
	switch s := strings.ToLower(strings.TrimSpace(status)); {
	case s == "":
		return ""
	case strings.Contains(s, "progress"), strings.Contains(s, "review"), s == "started", s == "doing":
		return "started"
	case s == "done", s == "completed", s == "complete", s == "merged", s == "closed", s == "shipped":
		return "done"
	case s == "canceled", s == "cancelled", s == "wontfix", s == "wont-do":
		return "canceled"
	case s == "todo", s == "unstarted":
		return "unstarted"
	case s == "backlog":
		return "backlog"
	default:
		return ""
	}
}

// fileBugBody builds the internal bug's description: a one-line provenance note
// followed by the QA verdict when its file can be read.
func fileBugBody(id, verdictPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Surfaced by the Trau loop while working on %s: its QA failed after automated repair and bugfix passes and needs human attention.\n", id)
	if verdictPath != "" {
		if data, err := os.ReadFile(verdictPath); err == nil {
			b.WriteString("\n---\n\n")
			b.Write(data)
		}
	}
	return b.String()
}

// labelsOf returns a single-element label slice, or nil for a blank label so a
// transition never sends an empty label delta.
func labelsOf(label string) []string {
	if strings.TrimSpace(label) == "" {
		return nil
	}
	return []string{label}
}

// issueNumber parses the trailing -N of an identifier (LOOP-12 → 12) for numeric
// ordering, returning 0 when there is no numeric suffix.
func issueNumber(id string) int {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		if n, err := strconv.Atoi(id[i+1:]); err == nil {
			return n
		}
	}
	return 0
}
