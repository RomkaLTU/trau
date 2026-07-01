package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RomkaLTU/trau/internal/tracker"
)

// sizeVerdict is the structured judgment of whether a ticket can realistically be
// built end-to-end inside one automated build window. The size judge writes it as
// JSON to sizeJudgePath(id); a false FitsOneWindow quarantines the ticket
// (unattended) or warns about it (attended).
type sizeVerdict struct {
	FitsOneWindow   bool     `json:"fits_one_window"`
	Reason          string   `json:"reason"`
	SuggestedSlices []string `json:"suggested_slices"`
}

// sizeSchema is the JSON skeleton shown to the judge so its verdict has one shape
// the guard can rely on.
const sizeSchema = `{"fits_one_window": true|false, "reason": "one line", "suggested_slices": ["...", "..."]}`

func sizeJudgePath(id string) string { return "/tmp/sizejudge-" + id + ".json" }

// sizeGuard sizes a fresh ticket before the build phase (runPhases gates it to a
// fresh start, so resumes never re-run it). A cheap structured LLM judge decides
// whether the ticket fits one build window; one that does not is quarantined,
// labeled with SplitLabel, and commented with the suggested seams on an autonomous
// or headless run, or surfaced as a non-blocking warning on an attended
// single-ticket run so the person watching can proceed or stop.
//
// The guard fails open: disabled, a tracker that cannot supply the ticket detail,
// an unrecoverable agent error, or an unparseable verdict all let the build
// proceed — it is a best-effort safety net, not a correctness gate. A provider
// pause or budget give-up during the judge still propagates so the loop handles it
// the same as any phase.
func (p *Pipeline) sizeGuard(ctx context.Context, id string) error {
	if !p.SizeJudge {
		return nil
	}
	detailer, ok := p.Tracker.(tracker.IssueDetailer)
	if !ok {
		return nil
	}
	detail, err := detailer.IssueDetail(ctx, id)
	if err != nil {
		p.logf("  size judge: could not read %s detail (skipping): %v", id, err)
		return nil
	}

	path := sizeJudgePath(id)
	_ = os.Remove(path)
	_, agentErr := p.agentStep(ctx, id, "sizejudge", sizeJudgeInstruction(id, detail, path))
	if agentErr != nil && isFatalAgentErr(agentErr) {
		return agentErr
	}
	v, ok := readSizeVerdict(path)
	if agentErr != nil || !ok {
		p.logf("  size judge: no usable verdict for %s (skipping)", id)
		return nil
	}
	p.persistSizeVerdict(id, v)
	if v.FitsOneWindow {
		return nil
	}
	if p.Attended {
		p.warnTooLarge(id, v)
		return nil
	}
	return p.quarantineTooLarge(ctx, id, v)
}

// warnTooLarge surfaces a too-large verdict without blocking: it prints the reason
// and the suggested split, then lets the build proceed. Used only for an attended
// single-ticket run, where the person watching can stop the run themselves.
func (p *Pipeline) warnTooLarge(id string, v sizeVerdict) {
	p.logf("  ⚠ size judge flags %s as likely too large for one build window", id)
	if r := strings.TrimSpace(v.Reason); r != "" {
		p.logf("      %s", r)
	}
	for i, s := range trimmedSlices(v.SuggestedSlices) {
		p.logf("      split %d) %s", i+1, s)
	}
	p.logf("  ▸ attended run — building anyway; stop the run to split it instead")
}

// quarantineTooLarge stops a too-large ticket on an unattended run: it applies the
// split label (before quarantine, so the quarantine's label update preserves it),
// then quarantines with a reason that carries the suggested seams. It returns the
// *GiveUpError from giveUp so classifyPhaseErr treats it as a verified dead end
// (giveUp is idempotent, so the flow through handleGiveUp does not re-quarantine).
func (p *Pipeline) quarantineTooLarge(ctx context.Context, id string, v sizeVerdict) error {
	if labeler, ok := p.Tracker.(tracker.IssueLabeler); ok && strings.TrimSpace(p.SplitLabel) != "" {
		if err := labeler.AddLabel(ctx, id, p.SplitLabel); err != nil {
			p.logf("  split-label error (continuing): %v", err)
		}
	}
	p.logf("  ✂ %s looks too large for one build window — quarantining for a human to split", id)
	return p.giveUp(ctx, id, sizeReason(v))
}

// sizeReason renders the quarantine reason (and comment) from a verdict: the
// judge's one-line reason plus the numbered suggested split seams.
func sizeReason(v sizeVerdict) string {
	reason := strings.TrimSpace(v.Reason)
	if reason == "" {
		reason = "the ticket is too large to build in one window"
	}
	msg := "ticket too large for one build window — " + reason
	if slices := trimmedSlices(v.SuggestedSlices); len(slices) > 0 {
		parts := make([]string, len(slices))
		for i, s := range slices {
			parts[i] = fmt.Sprintf("%d) %s", i+1, s)
		}
		msg += "; suggested split: " + strings.Join(parts, " ")
	}
	return msg
}

// trimmedSlices drops blank entries from the judge's suggested slices.
func trimmedSlices(slices []string) []string {
	out := make([]string, 0, len(slices))
	for _, s := range slices {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// persistSizeVerdict writes a durable copy of the verdict to runs/<id>/sizejudge.json
// so post-run consumers (the cost-anomaly flag, a resumed cleanup gate) can key off
// fits_one_window after /tmp is gone. Best-effort, like persistHandoff.
func (p *Pipeline) persistSizeVerdict(id string, v sizeVerdict) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	dir := filepath.Join(p.RunsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "sizejudge.json"), data, 0o644)
}

func readSizeVerdict(path string) (sizeVerdict, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sizeVerdict{}, false
	}
	var v sizeVerdict
	if err := json.Unmarshal(data, &v); err != nil {
		return sizeVerdict{}, false
	}
	return v, true
}

// sizeJudgeInstruction asks the judge to size the ticket from its title and
// description (which embeds the acceptance criteria) and write a structured
// verdict to exactly path. It reasons from the supplied text alone — no tools —
// so it stays a single cheap call routed to the pick-phase provider.
func sizeJudgeInstruction(id string, detail tracker.IssueDetail, path string) string {
	desc := strings.TrimSpace(detail.Description)
	if desc == "" {
		desc = "(no description)"
	}
	return "Size the engineering ticket " + id + " below. Decide whether ONE coding agent could realistically build it end-to-end — implementation and its tests — inside a single uninterrupted automated build window (one agent pass, on the order of ~15 minutes), without splitting it. Weigh the number and independence of acceptance criteria, how many files/layers it spans, and any browser or integration work. When it does NOT fit, give the natural split seams as separate, independently-shippable slices.\n\n" +
		"TITLE: " + strings.TrimSpace(detail.Title) + "\n\nDESCRIPTION:\n" + desc + "\n\n" +
		"Write your verdict as JSON to exactly " + path + " (overwrite if present) and nowhere else, as raw JSON with no markdown fences or prose, with this exact shape: " + sizeSchema +
		". Set fits_one_window=true when it comfortably fits (leave suggested_slices empty); false when it does not, with reason a single line and suggested_slices the ordered split seams. Do not modify any code or open the ticket in a tracker — judge only from the text above."
}
