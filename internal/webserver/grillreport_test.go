package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

const (
	reportTitleText = "How the SDK retries"
	reportFindings  = "# Question\n\nWhich retry policy?\n\n## Conclusion\n\nThe SDK backs off exponentially."
)

// seedResearchReport stands a research session up in the state the Research page
// reads a report from: an outcome carrying the findings, and the session settled on
// the state the caller names.
func seedResearchReport(t *testing.T, stores *hubstore.Stores, root, state string, outcome grillOutcome) int64 {
	t.Helper()
	sess, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: root, Mode: hubstore.GrillModeResearch})
	if err != nil {
		t.Fatalf("create research session: %v", err)
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	if _, _, err := stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindOutcome,
		Payload: string(body),
	}); err != nil {
		t.Fatalf("append outcome: %v", err)
	}
	if state == "" {
		return sess.ID
	}
	// applied is only ever reached through finished — approving a report the user is
	// reviewing — so the seed walks the same path the state machine allows.
	steps := []string{hubstore.GrillFinished}
	if state != hubstore.GrillFinished {
		steps = append(steps, state)
	}
	for _, step := range steps {
		if _, err := stores.Grill().Transition(sess.ID, step, ""); err != nil {
			t.Fatalf("settle session on %s: %v", step, err)
		}
	}
	return sess.ID
}

func researchOutcome() grillOutcome {
	return grillOutcome{
		Disposition: grillDispResearch,
		Title:       reportTitleText,
		Findings:    reportFindings,
		Sources: []grillSource{
			{Title: "SDK retry policy", URL: "https://example.com/docs", Note: "states the backoff schedule"},
		},
		Summary: "The SDK backs off exponentially.",
	}
}

func openingNoteText(t *testing.T, stores *hubstore.Stores, sid int64) string {
	t.Helper()
	msgs, err := stores.Grill().Messages(sid, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("session opened with %d messages, want 1", len(msgs))
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(msgs[0].Payload), &payload); err != nil {
		t.Fatalf("decode opening note: %v", err)
	}
	return payload.Text
}

// Draft an issue opens an unanchored interview whose visible thread carries one short
// note naming and linking the report. The findings stay out of it — they reach the
// agent through the first-turn prompt alone.
func TestGrillCreateFromReportOpensLinkedDraft(t *testing.T) {
	ts, stores, repoName, srv := grillHookServer(t)
	repo, ok := srv.findRepo(repoName)
	if !ok {
		t.Fatalf("registered repo %q is not resolvable", repoName)
	}
	source := seedResearchReport(t, stores, repo.Root, hubstore.GrillFinished, researchOutcome())

	view := createGrillWith(t, ts, repoName, GrillCreateRequest{
		FromSession: strconv.FormatInt(source, 10),
	})
	if view.Mode != hubstore.GrillModeInterview || view.IssueID != "" || view.AutoAccept {
		t.Fatalf("draft session = %+v, want an unanchored interview that asks its questions", view)
	}

	sid, err := strconv.ParseInt(view.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	note := openingNoteText(t, stores, sid)
	for _, want := range []string{
		"Draft an issue from the research report",
		reportTitleText,
		"/research?repo=" + repoName + "&session=" + strconv.FormatInt(source, 10),
	} {
		if !strings.Contains(note, want) {
			t.Errorf("opening note %q is missing %q", note, want)
		}
	}
	if strings.Contains(note, "backs off exponentially") {
		t.Errorf("opening note dumped the findings into the thread: %q", note)
	}

	// The report is left exactly as it was, and a second click drafts again.
	src, found, err := stores.Grill().Session(source)
	if err != nil || !found {
		t.Fatalf("reload source session: %v", err)
	}
	if src.State != hubstore.GrillFinished || src.Mode != hubstore.GrillModeResearch {
		t.Fatalf("source session = %+v, want an untouched finished research session", src)
	}
	second := createGrillWith(t, ts, repoName, GrillCreateRequest{
		FromSession: strconv.FormatInt(source, 10),
	})
	if second.ID == view.ID {
		t.Fatal("a second Draft an issue reused the first draft")
	}
}

// An archived report is still a report: the act stays available after the research
// has been approved.
func TestGrillCreateFromAppliedReport(t *testing.T) {
	ts, stores, repoName, srv := grillHookServer(t)
	repo, _ := srv.findRepo(repoName)
	source := seedResearchReport(t, stores, repo.Root, hubstore.GrillApplied, researchOutcome())

	view := createGrillWith(t, ts, repoName, GrillCreateRequest{
		FromSession: strconv.FormatInt(source, 10),
	})
	sid, _ := strconv.ParseInt(view.ID, 10, 64)
	if note := openingNoteText(t, stores, sid); !strings.Contains(note, reportTitleText) {
		t.Fatalf("opening note %q does not name the archived report", note)
	}
}

// Everything that is not a finished report is refused before a session exists, and
// the refusal says which of the two things went wrong: the id names nothing (404), or
// it names something a draft cannot start from (400).
func TestGrillCreateFromReportRefusals(t *testing.T) {
	ts, stores, repoName, srv := grillHookServer(t)
	repo, _ := srv.findRepo(repoName)

	running := seedResearchReport(t, stores, repo.Root, "", researchOutcome())
	noReport := seedResearchReport(t, stores, repo.Root, hubstore.GrillFinished, grillOutcome{
		Disposition: grillDispNoChange,
		Summary:     "Nothing to research after all.",
	})
	elsewhere := seedResearchReport(t, stores, "/tmp/other-repo", hubstore.GrillFinished, researchOutcome())
	interview, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: repo.Root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create interview session: %v", err)
	}

	before, err := stores.Grill().List(repo.Root, "", "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}

	for _, tc := range []struct {
		name   string
		req    GrillCreateRequest
		status int
		want   string
	}{
		{"not a number", GrillCreateRequest{FromSession: "abc"}, http.StatusBadRequest, "invalid from_session"},
		{"unknown session", GrillCreateRequest{FromSession: "99999"}, http.StatusNotFound, "unknown source session"},
		{"another repo", GrillCreateRequest{FromSession: strconv.FormatInt(elsewhere, 10)}, http.StatusNotFound, "unknown source session"},
		{"an interview", GrillCreateRequest{FromSession: strconv.FormatInt(interview.ID, 10)}, http.StatusBadRequest, "from_session is not a research session"},
		{"still running", GrillCreateRequest{FromSession: strconv.FormatInt(running, 10)}, http.StatusBadRequest, "the research session has no report yet"},
		{"no report", GrillCreateRequest{FromSession: strconv.FormatInt(noReport, 10)}, http.StatusBadRequest, "the research session has no report yet"},
		{
			"anchored",
			GrillCreateRequest{FromSession: strconv.FormatInt(running, 10), IssueID: "COD-2"},
			http.StatusBadRequest,
			"from_session drafts a new issue, so it takes no issue_id",
		},
		{
			"another mode",
			GrillCreateRequest{FromSession: strconv.FormatInt(running, 10), Mode: hubstore.GrillModeResearch},
			http.StatusBadRequest,
			"from_session opens an interview, not a research session",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repoName+"/grill", tc.req)
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			if got := decodeError(t, res); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}

	after, err := stores.Grill().List(repo.Root, "", "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("refused creates left %d new sessions behind", len(after)-len(before))
	}
}

// The draft's first turn runs on the report prompt, carrying the whole findings and
// the sources the report cited — none of which the thread ever showed.
func TestGrillFirstPromptFromReport(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	source := seedResearchReport(t, r.srv.stores, repo.Root, hubstore.GrillFinished, researchOutcome())

	draft, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root})
	if err != nil {
		t.Fatalf("create draft session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(draft)) })
	report, err := r.srv.grillReportFor(repo.Root, source)
	if err != nil {
		t.Fatalf("load report: %v", err)
	}
	if _, _, err := store.AppendMessage(draft.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindInfo,
		Payload: `{"text":` + strconv.Quote(grillReportOpeningNote(report, repo.Name)) + `}`,
	}); err != nil {
		t.Fatalf("append opening note: %v", err)
	}

	got := r.firstPrompt(context.Background(), repo, draft)
	if !strings.HasPrefix(got, "You are helping the user turn a finished research report") {
		t.Fatalf("first turn is not the report prompt:\n%s", got)
	}
	for _, want := range []string{
		reportTitleText,
		"The SDK backs off exponentially.",
		"--- Sources the report cited ---",
		"https://example.com/docs",
		`disposition "create"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report prompt is missing %q:\n%s", want, got)
		}
	}

	// A report deleted after the draft started leaves an ordinary authoring session
	// opening on the note it kept.
	if _, err := store.Delete(source); err != nil {
		t.Fatalf("delete source report: %v", err)
	}
	if got := r.firstPrompt(context.Background(), repo, draft); !strings.HasPrefix(got, "You are helping the user turn a rough idea") {
		t.Fatalf("a deleted report did not fall back to the authoring prompt:\n%s", got)
	}
}

// A draft opened without a report is untouched by the report path — the plain
// authoring prompt still opens on the seed the user typed.
func TestGrillFirstPromptWithoutReportStaysAuthoring(t *testing.T) {
	r, store, repo, _ := newGrillRunnerTest(t, grillStubScript)
	draft, err := store.Create(hubstore.NewGrillSession{Repo: repo.Root})
	if err != nil {
		t.Fatalf("create draft session: %v", err)
	}
	t.Cleanup(func() { attachfile.Remove(grillAttachTicket(draft)) })
	if _, _, err := store.AppendMessage(draft.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleUser,
		Kind:    hubstore.GrillKindInfo,
		Payload: `{"text":"A dark-mode toggle in the toolbar."}`,
	}); err != nil {
		t.Fatalf("append seed: %v", err)
	}
	got := r.firstPrompt(context.Background(), repo, draft)
	if !strings.HasPrefix(got, "You are helping the user turn a rough idea") {
		t.Fatalf("seeded draft did not run the authoring prompt:\n%s", got)
	}
	if !strings.Contains(got, "A dark-mode toggle in the toolbar.") {
		t.Errorf("authoring prompt dropped the seed:\n%s", got)
	}
}

func TestGrillReportSourceIDReadsTheNoteBack(t *testing.T) {
	note := grillReportOpeningNote(grillReport{sid: 42, title: reportTitleText}, "acme")
	sid, ok := grillReportSourceID(note)
	if !ok || sid != 42 {
		t.Fatalf("grillReportSourceID(%q) = (%d, %v), want (42, true)", note, sid, ok)
	}
	for _, other := range []string{
		"A dark-mode toggle in the toolbar.",
		"See /research for the report.",
		"Propose a fix for COD-1: verify dead end",
	} {
		if _, ok := grillReportSourceID(other); ok {
			t.Errorf("%q read as a report link", other)
		}
	}
}
