package tracker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// sourceSynced is the backlog-query source that selects a repo's synced tracker
// tickets — every non-internal row — for the store-backed picker.
const sourceSynced = "synced"

// storeHub is the slice of the hub API the store-backed tracker drives: everything
// the internal provider needs, plus reading a synced issue and the backlog from the
// store, mirroring a tracker write onto a synced row, and nudging a sync.
// *hubclient.Client satisfies it; tests supply a fake.
type storeHub interface {
	hubAPI
	Sync(ctx context.Context, repo string) error
	Issue(ctx context.Context, repo, id string) (hubclient.Issue, error)
	MirrorSynced(ctx context.Context, repo, id string, m hubclient.SyncedMirror) error
}

// StoreBacked is the pipeline's tracker for a synced repo (Linear or Jira): every
// read — pick eligibility, prompt content, status, parent, project — comes from the
// hub's issue store over HTTP, so a loop run makes no tracker read calls (ADR 0007).
// Writes are unchanged in kind: status transitions and label changes go directly to
// the external tracker through Writes, and each is mirrored onto the store row in the
// same motion so the board never lags a transition. Bugs the verify loop files become
// internal issues, never external ones.
//
// A synced repo's store also holds internal issues — ones the verify loop filed, or
// the hub minted — carrying an identifier prefix the tracker knows nothing about.
// Every operation naming such an id is served by the internal provider instead, so
// an internal ticket runs here the way it does in a repo with no tracker at all.
type StoreBacked struct {
	Writes          Tracker
	Hub             storeHub
	Repo            string
	InternalPrefix  string
	ReadyLabel      string
	QuarantineLabel string
}

// NewStoreBacked wraps writes — the repo's external tracker — so its reads come from
// the hub store while its status/label writes still land on the tracker (and, in the
// same call, on the store row). internalPrefix is the prefix the repo's internal
// issue ids carry; "" leaves every id with the tracker.
func NewStoreBacked(writes Tracker, hub storeHub, repo, internalPrefix, readyLabel, quarantineLabel string) *StoreBacked {
	return &StoreBacked{
		Writes:          writes,
		Hub:             hub,
		Repo:            repo,
		InternalPrefix:  internalPrefix,
		ReadyLabel:      readyLabel,
		QuarantineLabel: quarantineLabel,
	}
}

// isInternal reports whether id names one of the repo's internal issues rather than
// a synced ticket. The hub mints internal ids with a prefix that is never the
// tracker's team key, so the identifier alone decides. No id — an unscoped pick —
// is never internal, whatever prefix prefixOf falls back to.
func (in *StoreBacked) isInternal(id string) bool {
	if id == "" || in.InternalPrefix == "" {
		return false
	}
	return strings.EqualFold(prefixOf(id), in.InternalPrefix)
}

// internal is the provider that serves the repo's internal issues, over the same hub.
func (in *StoreBacked) internal() *Internal {
	return &Internal{
		Hub:             in.Hub,
		Repo:            in.Repo,
		ReadyLabel:      in.ReadyLabel,
		QuarantineLabel: in.QuarantineLabel,
	}
}

// Pick nudges a sync so a ticket finished, reopened, or removed out-of-band cannot
// be picked from stale state, then returns the lowest-numbered ready, unstarted,
// leaf synced issue in scope — or "" when none is eligible.
func (in *StoreBacked) Pick(ctx context.Context, scope Scope) (string, error) {
	candidates, err := in.eligible(ctx, scope)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	return candidates[0].ID, nil
}

// ListEligible enumerates the synced issues Pick would consider, ordered the same
// way — the source for --list-eligible and the hub's eligible board.
func (in *StoreBacked) ListEligible(ctx context.Context, scope Scope) ([]ListedTicket, error) {
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

// eligible nudges a sync, then reads the repo's ready-labelled synced backlog and
// narrows it to the runnable candidates: an unstarted leaf, restricted to a scoped
// parent's children, ordered by ascending issue number. A pick scoped to an internal
// epic reads that epic's children instead; an unscoped pick stays synced-only, so the
// loop never picks an internal issue out of a tracker's backlog on its own.
func (in *StoreBacked) eligible(ctx context.Context, scope Scope) ([]hubclient.BacklogItem, error) {
	if in.isInternal(scope.Parent) {
		return in.internal().eligible(ctx, scope)
	}
	if err := in.Hub.Sync(ctx, in.Repo); err != nil {
		return nil, fmt.Errorf("sync before pick: %w", err)
	}
	items, err := in.Hub.Backlog(ctx, in.Repo, hubclient.BacklogQuery{Source: sourceSynced, Label: in.ReadyLabel})
	if err != nil {
		return nil, err
	}
	out := make([]hubclient.BacklogItem, 0, len(items))
	for _, it := range items {
		if it.HasChildren {
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

// SubIssues returns the synced children of id, ordered by issue number.
func (in *StoreBacked) SubIssues(ctx context.Context, id string) ([]SubIssue, error) {
	if in.isInternal(id) {
		return in.internal().SubIssues(ctx, id)
	}
	items, err := in.Hub.Backlog(ctx, in.Repo, hubclient.BacklogQuery{Source: sourceSynced})
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
			Done:        it.Group == "completed" || it.Group == "canceled",
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

// Title returns the title of synced issue id from the store.
func (in *StoreBacked) Title(ctx context.Context, id string) (string, error) {
	iss, err := in.Hub.Issue(ctx, in.Repo, id)
	if err != nil {
		return "", err
	}
	return iss.Title, nil
}

// IssueDetail returns the title, description, and comments of synced issue id from
// the store, for the build/verify prompt context.
func (in *StoreBacked) IssueDetail(ctx context.Context, id string) (IssueDetail, error) {
	iss, err := in.Hub.Issue(ctx, in.Repo, id)
	if err != nil {
		return IssueDetail{}, err
	}
	detail := IssueDetail{Title: iss.Title, Description: iss.Description}
	for _, c := range iss.Comments {
		detail.Comments = append(detail.Comments, IssueComment{Author: c.Author, Body: c.Body})
	}
	return detail, nil
}

// IssueStatus reports the normalized lifecycle status of synced issue id from its
// stored status group. A missing issue yields StatusUnknown so a --status reconcile
// leaves the checkpoint intact rather than clearing live work.
func (in *StoreBacked) IssueStatus(ctx context.Context, id string) (IssueStatus, error) {
	iss, err := in.Hub.Issue(ctx, in.Repo, id)
	if err != nil {
		if errors.Is(err, hubclient.ErrNotFound) {
			return StatusUnknown, nil
		}
		return StatusUnknown, err
	}
	switch iss.Group {
	case "completed", "done":
		return StatusDone, nil
	case "canceled":
		return StatusCanceled, nil
	default:
		return StatusOpen, nil
	}
}

// ParentIssue reports the epic a synced issue belongs to, or "" when it is
// top-level. A missing issue reads as no parent so epic stacking never forces an
// unwanted branch.
func (in *StoreBacked) ParentIssue(ctx context.Context, id string) (string, error) {
	iss, err := in.Hub.Issue(ctx, in.Repo, id)
	if err != nil {
		if errors.Is(err, hubclient.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return iss.Parent, nil
}

// IssueProject reports the tracker project of id when it lies outside the repo's
// Project, so the ownership guard refuses a cross-project ticket. A stored issue is
// in-project by construction, so it returns "" — the guard's no-op. The by-id read
// resolves an unstored id through the hub's tracker fallback, so a run-once ticket
// from another project is caught here (ADR 0007).
func (in *StoreBacked) IssueProject(ctx context.Context, id string) (string, error) {
	iss, err := in.Hub.Issue(ctx, in.Repo, id)
	if err != nil {
		return "", err
	}
	if iss.InProject {
		return "", nil
	}
	return iss.Project, nil
}

// SetStatus moves the ticket in the external tracker and mirrors the new status onto
// the store row so the board reflects it at once.
func (in *StoreBacked) SetStatus(ctx context.Context, id, status, extra string) error {
	if in.isInternal(id) {
		return in.internal().SetStatus(ctx, id, status, extra)
	}
	if err := in.Writes.SetStatus(ctx, id, status, extra); err != nil {
		return err
	}
	in.mirror(ctx, id, hubclient.SyncedMirror{Status: status, StatusGroup: syncedStatusGroup(status)})
	return nil
}

// Reset returns the ticket to an unstarted, ready state in the tracker and mirrors
// the ready/quarantine label swap and the unstarted group onto the store row.
func (in *StoreBacked) Reset(ctx context.Context, id string) error {
	if in.isInternal(id) {
		return in.internal().Reset(ctx, id)
	}
	if err := in.Writes.Reset(ctx, id); err != nil {
		return err
	}
	in.mirror(ctx, id, hubclient.SyncedMirror{
		StatusGroup:  "unstarted",
		AddLabels:    labelsOf(in.ReadyLabel),
		RemoveLabels: labelsOf(in.QuarantineLabel),
	})
	return nil
}

// Quarantine marks the ticket unrecoverable in the tracker and mirrors the
// quarantine/ready label swap onto the store row.
func (in *StoreBacked) Quarantine(ctx context.Context, id, reason string) error {
	if in.isInternal(id) {
		return in.internal().Quarantine(ctx, id, reason)
	}
	if err := in.Writes.Quarantine(ctx, id, reason); err != nil {
		return err
	}
	in.mirror(ctx, id, hubclient.SyncedMirror{
		AddLabels:    labelsOf(in.QuarantineLabel),
		RemoveLabels: labelsOf(in.ReadyLabel),
	})
	return nil
}

// AddLabel adds one label in the tracker and mirrors it onto the store row. A
// tracker that cannot add a single label makes the write a no-op.
func (in *StoreBacked) AddLabel(ctx context.Context, id, label string) error {
	if strings.TrimSpace(label) == "" {
		return nil
	}
	if in.isInternal(id) {
		return in.internal().AddLabel(ctx, id, label)
	}
	if labeler, ok := in.Writes.(IssueLabeler); ok {
		if err := labeler.AddLabel(ctx, id, label); err != nil {
			return err
		}
	}
	in.mirror(ctx, id, hubclient.SyncedMirror{AddLabels: []string{label}})
	return nil
}

// FileBug files the verify loop's HITL blocker as an internal issue, never in the
// external tracker (ADR 0007): trau does not create issues in a synced tracker. It
// carries the QA verdict and needs-human labels, and is not marked ready so the loop
// never re-picks it.
func (in *StoreBacked) FileBug(ctx context.Context, id, verdictPath string) (string, error) {
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

// EnsureLabels provisions the managed labels on the external tracker — synced
// issues carry real tracker labels, so they must exist there.
func (in *StoreBacked) EnsureLabels(ctx context.Context) error {
	return in.Writes.EnsureLabels(ctx)
}

// ListTeams enumerates the external tracker's selectable teams/projects for the
// onboarding wizard; the store holds no team catalog, so it delegates to Writes.
func (in *StoreBacked) ListTeams(ctx context.Context) ([]Team, error) {
	lister, ok := in.Writes.(TeamLister)
	if !ok {
		return nil, errors.New("tracker: team listing not supported")
	}
	return lister.ListTeams(ctx)
}

// mirror applies m to the synced issue's store row, best-effort: the tracker write
// it follows already succeeded, and the next sync reconciles the row, so a failed
// mirror never fails the operation.
func (in *StoreBacked) mirror(ctx context.Context, id string, m hubclient.SyncedMirror) {
	_ = in.Hub.MirrorSynced(ctx, in.Repo, id, m)
}

// syncedStatusGroup maps a pipeline display status onto the stored status group so a
// mirrored transition buckets on the board the way a real sync would. An
// unrecognized status returns "" — the store leaves the group unchanged.
func syncedStatusGroup(status string) string {
	switch s := strings.ToLower(strings.TrimSpace(status)); {
	case s == "":
		return ""
	case strings.Contains(s, "progress"), strings.Contains(s, "review"), s == "started", s == "doing":
		return "started"
	case s == "done", s == "completed", s == "complete", s == "merged", s == "closed", s == "shipped":
		return "completed"
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
