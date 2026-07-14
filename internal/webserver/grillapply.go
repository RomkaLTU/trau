package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// Per-step outcomes an apply reports. A step is either attempted (ok/failed) or
// omitted from the list entirely when the disposition does not call for it.
const (
	grillStepOK     = "ok"
	grillStepFailed = "failed"
)

// grillTriageLabels are the inbox qualifiers a grilled issue leaves behind once it
// has been clarified — it is no longer waiting on triage. They are fixed, not
// config-driven: the triage inbox keys on them by name.
var grillTriageLabels = []string{"needs-triage", "needs-info"}

// GrillApplyRequest is the body of POST /grill/{sid}/apply. ProposedDescription is
// the possibly user-edited replacement from the review UI; empty falls back to the
// description the agent proposed in the outcome. SubIssues carries the user-edited
// split breakdown; nil falls back to the agent's proposal.
type GrillApplyRequest struct {
	ProposedDescription string          `json:"proposed_description"`
	SubIssues           []grillSubIssue `json:"sub_issues"`
}

// grillSubIssue is one proposed slice of a split: a fully-specified child issue
// with optional labels (defaulting to the ready label at apply time) and
// blocked_by indices referencing earlier siblings in the same proposal.
type grillSubIssue struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
	BlockedBy   []int    `json:"blocked_by,omitempty"`
}

// GrillApplyStep is one apply step's outcome. Error is set only when Status is
// failed.
type GrillApplyStep struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// GrillApplyResponse reports the session after an apply attempt, whether every step
// landed, and each step's outcome in the order they ran. Applied is true only when
// all steps succeeded; a partial apply leaves the session finished for re-apply.
type GrillApplyResponse struct {
	Session GrillSessionView `json:"session"`
	Applied bool             `json:"applied"`
	Steps   []GrillApplyStep `json:"steps"`
}

type grillOutcome struct {
	Disposition         string          `json:"disposition"`
	ProposedDescription string          `json:"proposed_description"`
	SubIssues           []grillSubIssue `json:"sub_issues"`
	Summary             string          `json:"summary"`
}

// handleGrillApply writes a finished session's proposed outcome to the tracker
// (POST /grill/{sid}/apply). Steps run in order — description, summary comment,
// label transition — each reported independently; the session settles as applied
// only when all of them succeed, so a partial failure stays finished and re-apply
// retries. Each landed step is mirrored onto the stored synced row so the next
// inbound sync sees no divergence (ADR 0007).
func (s *Server) handleGrillApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sid, ok := parseSID(w, r)
	if !ok {
		return
	}
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown grill session"})
		return
	}
	switch sess.State {
	case hubstore.GrillApplied:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session is already applied"})
		return
	case hubstore.GrillFinished:
	default:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no proposed outcome to apply"})
		return
	}

	var req GrillApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	outcome, ok := latestGrillOutcome(msgs)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no proposed outcome to apply"})
		return
	}

	if outcome.Disposition == grillDispNoChange {
		s.settleGrillApplied(w, &sess, nil)
		return
	}

	issueID := strings.TrimSpace(sess.IssueID)
	if issueID == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this grilling session is not anchored to an issue and has nothing to apply to",
		})
		return
	}
	repo, ok := s.findRepoByRoot(sess.Repo)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the session's repo is no longer registered"})
		return
	}
	cfg, writer, err := s.grillWriterFor(repo)
	if err != nil {
		writeWriterErr(w, err)
		return
	}

	description := strings.TrimSpace(req.ProposedDescription)
	if description == "" {
		description = outcome.ProposedDescription
	}
	comment := composeGrillSummary(outcome.Summary, msgs)

	var (
		steps   []GrillApplyStep
		applied bool
	)
	if outcome.Disposition == grillDispSplit {
		subs := req.SubIssues
		if len(subs) == 0 {
			subs = outcome.SubIssues
		}
		_, remove := grillLabelTransition(grillDispSplit, cfg)
		plan := grillSplitPlan{
			description:  description,
			comment:      comment,
			subIssues:    subs,
			readyLabel:   cfg.ReadyLabel,
			removeLabels: remove,
		}
		steps, applied = s.applyGrillSplit(r.Context(), writer, repo.Root, cfg.TrackerProvider, issueID, plan)
	} else {
		plan := grillApplyPlan{
			disposition: outcome.Disposition,
			description: description,
			comment:     comment,
		}
		plan.addLabels, plan.removeLabels = grillLabelTransition(outcome.Disposition, cfg)
		steps, applied = s.applyGrillOutcome(r.Context(), writer, repo.Root, issueID, plan)
	}
	if applied {
		s.settleGrillApplied(w, &sess, steps)
		return
	}
	writeJSON(w, http.StatusOK, GrillApplyResponse{Session: grillSessionView("", sess), Applied: false, Steps: steps})
}

// settleGrillApplied moves the session to applied and returns the apply response.
// A transition failure is logged but not fatal — the tracker writes already landed
// — and the session is returned as-is.
func (s *Server) settleGrillApplied(w http.ResponseWriter, sess *hubstore.GrillSession, steps []GrillApplyStep) {
	if applied, err := s.stores.Grill().Transition(sess.ID, hubstore.GrillApplied, ""); err == nil {
		*sess = applied
		s.publishGrillState(applied)
	} else {
		logger.Verbosef("grill apply %d: settle applied: %v", sess.ID, err)
	}
	if steps == nil {
		steps = []GrillApplyStep{}
	}
	writeJSON(w, http.StatusOK, GrillApplyResponse{Session: grillSessionView("", *sess), Applied: true, Steps: steps})
}

// grillApplyPlan is the resolved write plan for one disposition: the fields to
// write and the label delta. description is empty for needs_split.
type grillApplyPlan struct {
	disposition  string
	description  string
	comment      string
	addLabels    []string
	removeLabels []string
}

// applyGrillOutcome performs the plan's tracker writes in order, recording each
// step's outcome, and mirrors the steps that landed onto the stored synced row in
// one transaction so the board reflects the apply and the next sync sees no
// divergence. It reports whether every step succeeded.
func (s *Server) applyGrillOutcome(ctx context.Context, writer tracker.Writer, root, issueID string, plan grillApplyPlan) ([]GrillApplyStep, bool) {
	steps := make([]GrillApplyStep, 0, 3)
	patch := hubstore.SyncedPatch{}
	allOK := true
	record := func(name string, err error) {
		step := GrillApplyStep{Step: name, Status: grillStepOK}
		if err != nil {
			step.Status = grillStepFailed
			step.Error = err.Error()
			allOK = false
		}
		steps = append(steps, step)
	}

	if plan.disposition == grillDispRewrite {
		err := writer.UpdateDescription(ctx, issueID, plan.description)
		record("description", err)
		if err == nil {
			patch.Description = plan.description
		}
	}

	record("comment", writer.AddComment(ctx, issueID, plan.comment))

	if err := writer.UpdateLabels(ctx, issueID, plan.addLabels, plan.removeLabels); err != nil {
		record("labels", err)
	} else {
		record("labels", nil)
		patch.AddLabels = plan.addLabels
		patch.RemoveLabels = plan.removeLabels
	}

	if patch.Description != "" || len(patch.AddLabels) > 0 || len(patch.RemoveLabels) > 0 {
		if _, _, err := s.stores.Issues().UpdateSynced(root, issueID, patch); err != nil {
			logger.Verbosef("grill apply %s: mirror synced row: %v", issueID, err)
		}
	}
	return steps, allOK
}

// grillSplitPlan is the resolved write plan for a split: the parent's epic-framing
// description, the summary comment, the proposed sub-issues, the label a sub-issue
// defaults to, and the labels to strip from the parent now that it is a specified
// epic.
type grillSplitPlan struct {
	description  string
	comment      string
	subIssues    []grillSubIssue
	readyLabel   string
	removeLabels []string
}

// applyGrillSplit converts the parent into an epic and creates its proposed
// sub-issues. Steps run in order — parent description, one per sub-issue, sibling
// blocking relations, summary comment, parent labels — each reported independently,
// so a partial apply stays finished and re-apply retries. A sub-issue already
// present under the parent (matched by title in the store) is reused rather than
// created again, so a retry after a partial run adds only the missing slices and
// never a duplicate. Each created sub-issue is mirrored into the store as it lands,
// both for that dedup and so the next inbound sync sees no divergence (ADR 0007).
func (s *Server) applyGrillSplit(ctx context.Context, writer tracker.Writer, root, provider, parentID string, plan grillSplitPlan) ([]GrillApplyStep, bool) {
	steps := make([]GrillApplyStep, 0, len(plan.subIssues)+4)
	allOK := true
	record := func(name string, err error) {
		step := GrillApplyStep{Step: name, Status: grillStepOK}
		if err != nil {
			step.Status = grillStepFailed
			step.Error = err.Error()
			allOK = false
		}
		steps = append(steps, step)
	}

	patch := hubstore.SyncedPatch{}
	if err := writer.UpdateDescription(ctx, parentID, plan.description); err != nil {
		record("description", err)
	} else {
		record("description", nil)
		patch.Description = plan.description
	}

	existing := s.grillExistingChildren(root, parentID)
	ids := make([]string, len(plan.subIssues))
	for i, sub := range plan.subIssues {
		if id, ok := existing[strings.ToLower(strings.TrimSpace(sub.Title))]; ok {
			ids[i] = id
			record(grillSubIssueStep(sub.Title), nil)
			continue
		}
		labels := sub.Labels
		if len(labels) == 0 {
			labels = []string{plan.readyLabel}
		}
		created, err := writer.CreateIssue(ctx, tracker.IssueDraft{
			Title:       sub.Title,
			Description: sub.Description,
			Labels:      labels,
			Parent:      parentID,
		})
		record(grillSubIssueStep(sub.Title), err)
		if err != nil {
			continue
		}
		ids[i] = created.Identifier
		s.mirrorCreatedSubIssue(root, provider, parentID, created.Identifier, sub, labels)
	}

	if wired, relErr := s.wireGrillBlocks(ctx, writer, root, plan.subIssues, ids); wired {
		record("relations", relErr)
	}

	record("comment", writer.AddComment(ctx, parentID, plan.comment))

	if err := writer.UpdateLabels(ctx, parentID, nil, plan.removeLabels); err != nil {
		record("labels", err)
	} else {
		record("labels", nil)
		patch.RemoveLabels = plan.removeLabels
	}

	if patch.Description != "" || len(patch.RemoveLabels) > 0 {
		if _, _, err := s.stores.Issues().UpdateSynced(root, parentID, patch); err != nil {
			logger.Verbosef("grill apply %s: mirror synced row: %v", parentID, err)
		}
	}
	return steps, allOK
}

// grillExistingChildren maps a parent's already-created children by lowercased
// title to identifier, so a retry reuses a slice an earlier run created instead of
// filing it twice.
func (s *Server) grillExistingChildren(root, parentID string) map[string]string {
	children, err := s.stores.Issues().Children(root, parentID)
	if err != nil {
		logger.Verbosef("grill apply %s: read children: %v", parentID, err)
		return nil
	}
	out := make(map[string]string, len(children))
	for _, c := range children {
		out[strings.ToLower(strings.TrimSpace(c.Title))] = c.Identifier
	}
	return out
}

// mirrorCreatedSubIssue inserts a freshly created sub-issue into the store so the
// board shows it under the epic at once and the next inbound sync reconciles it in
// place rather than as a divergence (ADR 0007).
func (s *Server) mirrorCreatedSubIssue(root, provider, parentID, identifier string, sub grillSubIssue, labels []string) {
	if _, _, err := s.stores.Issues().Upsert(root, provider, []hubstore.Issue{{
		Identifier:  identifier,
		Title:       sub.Title,
		Description: sub.Description,
		StatusGroup: "unstarted",
		Labels:      labels,
		Parent:      parentID,
	}}); err != nil {
		logger.Verbosef("grill apply %s: mirror sub-issue %s: %v", parentID, identifier, err)
	}
}

// wireGrillBlocks files the blocking relations between sibling sub-issues: for each
// slice, every blocked_by sibling that now has an identifier blocks it. A relation
// already written on an earlier pass is skipped and one that lands is recorded, so
// a retry re-attempts only the relations that never landed — including one whose
// slice was created earlier but whose link write failed — and never duplicates a
// relation that did. It reports whether any relation was attempted and the first
// error, if any.
func (s *Server) wireGrillBlocks(ctx context.Context, writer tracker.Writer, root string, subs []grillSubIssue, ids []string) (attempted bool, err error) {
	done, loadErr := s.stores.Grill().BlockRelations(root)
	if loadErr != nil {
		logger.Verbosef("grill apply: read wired relations: %v", loadErr)
	}
	for i, sub := range subs {
		if ids[i] == "" {
			continue
		}
		for _, dep := range sub.BlockedBy {
			if dep < 0 || dep >= len(ids) || ids[dep] == "" || done[[2]string{ids[dep], ids[i]}] {
				continue
			}
			attempted = true
			if linkErr := writer.LinkBlocks(ctx, ids[dep], ids[i]); linkErr != nil {
				if err == nil {
					err = linkErr
				}
				continue
			}
			if markErr := s.stores.Grill().MarkBlockRelation(root, ids[dep], ids[i]); markErr != nil {
				logger.Verbosef("grill apply: record wired relation %s->%s: %v", ids[dep], ids[i], markErr)
			}
		}
	}
	return attempted, err
}

// grillSubIssueStep names the apply step for one proposed slice so the review UI
// can show which slice failed.
func grillSubIssueStep(title string) string {
	return "sub-issue: " + strings.TrimSpace(title)
}

// grillLabelTransition resolves the label delta for a disposition: a clarified
// issue leaves the triage inbox, then a rewrite becomes ready for an agent while a
// needs_split is flagged for splitting. A split turns the issue into a specified
// epic, so it sheds the split flag too and gains nothing — the readiness moves to
// its sub-issues.
func grillLabelTransition(disposition string, cfg config.Config) (add, remove []string) {
	remove = grillTriageLabels
	switch disposition {
	case grillDispRewrite:
		add = []string{cfg.ReadyLabel}
	case grillDispNeedsSplit:
		add = []string{cfg.SplitLabel}
	case grillDispSplit:
		remove = append(append([]string{}, grillTriageLabels...), cfg.SplitLabel)
	}
	return add, remove
}

// grillWriterFor resolves the repo's layered config and a direct tracker Writer,
// returning the config so the caller can read its label names.
func (s *Server) grillWriterFor(repo registry.Repo) (config.Config, tracker.Writer, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return config.Config{}, nil, err
	}
	writer, err := s.newWriter(cfg)
	return cfg, writer, err
}

// findRepoByRoot resolves a stored session's repo root back to a known repo,
// unioning live loops' repos the same way findRepo does.
func (s *Server) findRepoByRoot(root string) (registry.Repo, bool) {
	if root == "" {
		return registry.Repo{}, false
	}
	for _, repo := range s.knownRepos(s.liveInstances()) {
		if repo.Root == root {
			return repo, true
		}
	}
	return registry.Repo{}, false
}

// latestGrillOutcome returns the session's most recent finish_session outcome, the
// proposal the user is reviewing.
func latestGrillOutcome(msgs []hubstore.GrillMessage) (grillOutcome, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind != hubstore.GrillKindOutcome {
			continue
		}
		var o grillOutcome
		if err := json.Unmarshal([]byte(msgs[i].Payload), &o); err != nil {
			return grillOutcome{}, false
		}
		return o, true
	}
	return grillOutcome{}, false
}

// composeGrillSummary builds the summary comment from the session: the agent's
// summary line followed by the clarifications reached, each question paired with
// the user's answer in Q→A form.
func composeGrillSummary(summary string, msgs []hubstore.GrillMessage) string {
	var b strings.Builder
	b.WriteString("Grilling summary")
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString(": ")
		b.WriteString(s)
	}
	pairs := grillQAPairs(msgs)
	if len(pairs) > 0 {
		b.WriteString("\n\nClarifications:")
		for _, qa := range pairs {
			b.WriteString("\n\nQ: ")
			b.WriteString(qa.question)
			b.WriteString("\nA: ")
			b.WriteString(qa.answer)
		}
	}
	return b.String()
}

type grillQA struct{ question, answer string }

// grillQAPairs pairs each posed question with the answer that followed it, in order.
// An unanswered trailing question is kept with an empty answer.
func grillQAPairs(msgs []hubstore.GrillMessage) []grillQA {
	var pairs []grillQA
	pending := ""
	havePending := false
	for _, m := range msgs {
		switch m.Kind {
		case hubstore.GrillKindQuestion:
			if havePending {
				pairs = append(pairs, grillQA{question: pending})
			}
			pending = grillMessageText(m.Payload)
			havePending = true
		case hubstore.GrillKindAnswer:
			if havePending {
				pairs = append(pairs, grillQA{question: pending, answer: grillMessageText(m.Payload)})
				havePending = false
			}
		}
	}
	if havePending {
		pairs = append(pairs, grillQA{question: pending})
	}
	return pairs
}

// grillMessageText reads the "text" field of a question or answer payload.
func grillMessageText(payload string) string {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(payload), &p)
	return strings.TrimSpace(p.Text)
}
