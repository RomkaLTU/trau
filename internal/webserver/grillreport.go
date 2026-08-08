package webserver

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// grillReport is a finished research report as a draft interview reads it: the title
// the report is known by, the whole findings Markdown it delivered, and the sources
// it cited. The findings ground the first turn's prompt and never enter the visible
// thread, which carries only the note naming the report.
type grillReport struct {
	sid      int64
	title    string
	findings string
	sources  []grillSource
}

// errGrillReportMissing separates the source id that names nothing from the one that
// names the wrong thing: the first answers 404, every other refusal answers 400.
var errGrillReportMissing = errors.New("unknown source session")

// grillReportFor resolves the report a session id names inside one repo. The source
// must be a research session of that repo which has settled on a report: a session
// still running, one of another mode, or one whose latest outcome is not a report has
// nothing for a draft to start from. Follow-ups leave several outcomes behind, and
// the newest is the report the Research page shows and this reads.
func (s *Server) grillReportFor(root string, sid int64) (grillReport, error) {
	sess, found, err := s.stores.Grill().Session(sid)
	if err != nil {
		return grillReport{}, fmt.Errorf("load source session: %w", err)
	}
	if !found || sess.Repo != root {
		return grillReport{}, errGrillReportMissing
	}
	if grillEffectiveMode(sess.Mode) != hubstore.GrillModeResearch {
		return grillReport{}, errors.New("from_session is not a research session")
	}
	switch sess.State {
	case hubstore.GrillFinished, hubstore.GrillApplied:
	default:
		return grillReport{}, errors.New("the research session has no report yet")
	}
	msgs, err := s.stores.Grill().Messages(sid, 0)
	if err != nil {
		return grillReport{}, fmt.Errorf("load source session: %w", err)
	}
	outcome, ok := latestGrillOutcome(msgs)
	if !ok || outcome.Disposition != grillDispResearch || strings.TrimSpace(outcome.Findings) == "" {
		return grillReport{}, errors.New("the research session has no report yet")
	}
	return grillReport{
		sid:      sid,
		title:    grillReportTitle(sess, outcome),
		findings: strings.TrimSpace(outcome.Findings),
		sources:  outcome.Sources,
	}, nil
}

// grillReportTitle names a report the way the Research page does (reportTitle in
// web/src/lib/report-export.ts): the name the user gave it outranks the one the
// outcome proposed, which outranks the seed the session opened on.
func grillReportTitle(sess hubstore.GrillSession, outcome grillOutcome) string {
	for _, candidate := range []string{sess.ReportTitle, outcome.Title, sess.IssueTitle} {
		if t := strings.TrimSpace(candidate); t != "" {
			return t
		}
	}
	return "Untitled research"
}

// grillReportOpeningNote is the line a draft interview started from a report opens on.
// It is the whole visible provenance — the findings stay in the prompt — and it is
// also where the link back to the report lives, so it carries the report's deep link
// in the form push notifications use (notificationURL in push.go). The report may be
// deleted later and the link go stale; the title still says what the draft came from.
func grillReportOpeningNote(report grillReport, repo string) string {
	link := "/research?" + url.Values{
		"session": {strconv.FormatInt(report.sid, 10)},
		"repo":    {repo},
	}.Encode()
	return "Draft an issue from the research report " + strconv.Quote(report.title) + " — " + link
}

// grillReportLink finds the report deep link inside an opening note. Provenance is
// one-way and adds no column, so the note the session opens on is the only record of
// where the draft came from, and the runner reads the source back out of it.
var grillReportLink = regexp.MustCompile(`/research\?[^\s]*session=(\d+)`)

// grillReportSourceID reports the research session an opening note was drafted from.
func grillReportSourceID(note string) (int64, bool) {
	m := grillReportLink.FindStringSubmatch(note)
	if m == nil {
		return 0, false
	}
	sid, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || sid <= 0 {
		return 0, false
	}
	return sid, true
}

// grillReportSourceSection renders the sources a report cited for the first-turn
// prompt, numbered as the report itself numbers them. A report settled from the
// repository alone cites none and renders nothing.
func grillReportSourceSection(sources []grillSource) string {
	if len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n--- Sources the report cited ---\n")
	for i, src := range sources {
		fmt.Fprintf(&b, "%d. %s — %s", i+1, strings.TrimSpace(src.Title), strings.TrimSpace(src.URL))
		if note := strings.TrimSpace(src.Note); note != "" {
			b.WriteString(" — " + note)
		}
		b.WriteString("\n")
	}
	return b.String()
}
