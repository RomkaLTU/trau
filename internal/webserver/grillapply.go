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
// description the agent proposed in the outcome.
type GrillApplyRequest struct {
	ProposedDescription string `json:"proposed_description"`
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
	Disposition         string `json:"disposition"`
	ProposedDescription string `json:"proposed_description"`
	Summary             string `json:"summary"`
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
	plan := grillApplyPlan{
		disposition: outcome.Disposition,
		description: description,
		comment:     composeGrillSummary(outcome.Summary, msgs),
	}
	plan.addLabels, plan.removeLabels = grillLabelTransition(outcome.Disposition, cfg)

	steps, applied := s.applyGrillOutcome(r.Context(), writer, repo.Root, issueID, plan)
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

// grillLabelTransition resolves the label delta for a disposition: a clarified
// issue leaves the triage inbox, then a rewrite becomes ready for an agent while a
// needs_split is flagged for splitting.
func grillLabelTransition(disposition string, cfg config.Config) (add, remove []string) {
	remove = grillTriageLabels
	switch disposition {
	case grillDispRewrite:
		add = []string{cfg.ReadyLabel}
	case grillDispNeedsSplit:
		add = []string{cfg.SplitLabel}
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
