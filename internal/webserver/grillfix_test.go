package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// grillFixServer isolates TMPDIR on top of the shared grill fixture: a fix session
// materializes its dossier into the process temp directory, which the test reads back.
func grillFixServer(t *testing.T) (*httptest.Server, *Server, registry.Repo) {
	t.Helper()
	ts, _, name, srv := grillHookServer(t)
	t.Setenv("TMPDIR", t.TempDir())
	repo, ok := srv.findRepo(name)
	if !ok {
		t.Fatalf("registered repo %q is not resolvable", name)
	}
	return ts, srv, repo
}

func seedRunCheckpoint(t *testing.T, srv *Server, root, ticket string, data map[string]string) {
	t.Helper()
	if err := srv.stores.Checkpoints().Upsert(root, ticket, data); err != nil {
		t.Fatalf("seed checkpoint %s: %v", ticket, err)
	}
}

func quarantinedCheckpoint(reason string) map[string]string {
	return map[string]string{
		"PHASE":          state.Quarantined,
		"TITLE":          "Toolbar collapses on narrow screens",
		"BRANCH":         "feature/COD-1-toolbar",
		"PR":             "42",
		"PR_URL":         "https://github.com/acme/repo/pull/42",
		"FAILURE_REASON": reason,
		"ANOMALIES":      "verify burned 3.20 USD, 4x the phase median",
	}
}

func TestGrillValidateModeAcceptsFix(t *testing.T) {
	mode, errMsg := grillValidateMode(hubstore.GrillModeFix)
	if mode != hubstore.GrillModeFix || errMsg != "" {
		t.Fatalf("grillValidateMode(fix) = (%q, %q), want (fix, \"\")", mode, errMsg)
	}
	_, errMsg = grillValidateMode("repair")
	for _, want := range []string{`"repair"`, hubstore.GrillModeInterview, hubstore.GrillModeResearch, hubstore.GrillModeFix} {
		if !strings.Contains(errMsg, want) {
			t.Errorf("rejection %q does not mention %q", errMsg, want)
		}
	}
}

// A fix session diagnoses one ticket's failed run, so an unanchored create and a
// ticket whose run is no longer failed are both refused before a session exists.
func TestGrillCreateFixRefusesWithoutFailedRun(t *testing.T) {
	ts, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-2", map[string]string{"PHASE": state.Building})
	seedRunCheckpoint(t, srv, repo.Root, "COD-3", map[string]string{
		"PHASE":          state.Building,
		"FAILURE_CLASS":  state.FailPaused,
		"FAILURE_REASON": "provider rate limit",
	})

	for _, tc := range []struct{ name, issue, want string }{
		{"unanchored", "", "fix mode requires an issue_id"},
		{"no checkpoint", "COD-9", "COD-9 is no longer in a failed state"},
		{"still running", "COD-2", "COD-2 is no longer in a failed state"},
		{"blamelessly paused", "COD-3", "COD-3 is no longer in a failed state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/repos/"+repo.Name+"/grill", GrillCreateRequest{
				IssueID: tc.issue,
				Mode:    hubstore.GrillModeFix,
			})
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
			if got := decodeError(t, res); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}

	sessions, err := srv.stores.Grill().List(repo.Root, "", "")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("a refused create left %d sessions behind", len(sessions))
	}
}

// The create compiles the dossier and opens the conversation on a readable line
// before the first turn spawns, so the session is grounded the moment it appears.
func TestGrillCreateFixSeedsDossierAndOpeningNote(t *testing.T) {
	ts, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-1", quarantinedCheckpoint("verify failed three times on the same assertion"))
	if err := srv.stores.PhaseLogs().Upsert(repo.Root, "COD-1", "verify", "assertion failed at line 12\n"); err != nil {
		t.Fatalf("seed phase log: %v", err)
	}

	view := createGrillWith(t, ts, repo.Name, GrillCreateRequest{
		IssueID:    "COD-1",
		Mode:       hubstore.GrillModeFix,
		AutoAccept: true,
	})
	if view.Mode != hubstore.GrillModeFix {
		t.Fatalf("session mode = %q, want fix", view.Mode)
	}

	dossier, err := os.ReadFile(grillDossierPath("COD-1"))
	if err != nil {
		t.Fatalf("read dossier: %v", err)
	}
	for _, want := range []string{state.FailGaveUp, "feature/COD-1-toolbar", "phase-verify.log"} {
		if !strings.Contains(string(dossier), want) {
			t.Errorf("dossier is missing %q:\n%s", want, dossier)
		}
	}

	sid, _ := strconv.ParseInt(view.ID, 10, 64)
	msgs, err := srv.stores.Grill().Messages(sid, 0)
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
	want := "Propose a fix for COD-1: verify failed three times on the same assertion"
	if payload.Text != want {
		t.Fatalf("opening note = %q, want %q", payload.Text, want)
	}
}

// A faulted run carries no needs-human label and often no branch; the flow reads the
// failure class alone, so it still qualifies.
func TestGrillFailedRunForReadsFaultedRun(t *testing.T) {
	_, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-4", map[string]string{
		"PHASE":          state.Building,
		"FAILURE_CLASS":  state.FailFaulted,
		"FAILURE_REASON": "provider returned a malformed stream",
	})

	run, ok := srv.grillFailedRunFor(repo.Root, "COD-4")
	if !ok {
		t.Fatal("a faulted run did not qualify for a fix session")
	}
	if run.FailureClass != state.FailFaulted || run.Branch != "" {
		t.Fatalf("run = %+v, want a branchless faulted run", run)
	}
	if got := grillFixOpeningNote(run); got != "Propose a fix for COD-4: provider returned a malformed stream" {
		t.Fatalf("opening note = %q", got)
	}
}

func TestWriteGrillDossierCarriesTheWholeRun(t *testing.T) {
	_, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-1", quarantinedCheckpoint("verify dead end"))
	for phase, content := range map[string]string{
		"build":  "wrote three files\n",
		"verify": "assertion failed at line 12\n",
	} {
		if err := srv.stores.PhaseLogs().Upsert(repo.Root, "COD-1", phase, content); err != nil {
			t.Fatalf("seed phase log %s: %v", phase, err)
		}
	}
	for kind, content := range map[string]string{
		hubstore.ArtifactVerdict:    `{"result":"fail","fails":["toolbar still collapses"]}`,
		hubstore.ArtifactHandoff:    "The toolbar must stay one row down to 320px.",
		hubstore.ArtifactRubric:     `{"acceptance_criteria":["no collapse at 320px"]}`,
		hubstore.ArtifactBuildNotes: "Touched toolbar.tsx.",
	} {
		if err := srv.stores.Artifacts().Upsert(repo.Root, "COD-1", kind, content); err != nil {
			t.Fatalf("seed artifact %s: %v", kind, err)
		}
	}
	for _, lesson := range []hubstore.Lesson{
		{Ticket: "COD-1", Phase: "verify", FailureType: "test", Lesson: "The breakpoint is set in two places."},
		{Ticket: "COD-8", Lesson: "Another ticket's lesson."},
	} {
		if err := srv.stores.Lessons().Append(repo.Root, lesson); err != nil {
			t.Fatalf("seed lesson: %v", err)
		}
	}

	run, ok := srv.grillFailedRunFor(repo.Root, "COD-1")
	if !ok {
		t.Fatal("quarantined run did not qualify")
	}
	if err := srv.writeGrillDossier(repo.Root, run); err != nil {
		t.Fatalf("write dossier: %v", err)
	}

	body := readFileString(t, grillDossierPath("COD-1"))
	dir := attachfile.Dir("COD-1")
	for _, want := range []string{
		state.Quarantined,
		state.FailGaveUp,
		"verify dead end",
		"feature/COD-1-toolbar",
		"https://github.com/acme/repo/pull/42",
		"verify burned 3.20 USD",
		filepath.Join(dir, "phase-build.log"),
		filepath.Join(dir, "phase-verify.log"),
		"toolbar still collapses",
		"The toolbar must stay one row down to 320px.",
		"no collapse at 320px",
		"Touched toolbar.tsx.",
		"The breakpoint is set in two places. (verify/test)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dossier is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Another ticket's lesson.") {
		t.Error("dossier carried a lesson from a different ticket")
	}
	if got := readFileString(t, filepath.Join(dir, "phase-verify.log")); got != "assertion failed at line 12\n" {
		t.Errorf("phase log = %q", got)
	}
}

// An early fault records no logs, artifacts or lessons. The dossier says so rather
// than failing the create.
func TestWriteGrillDossierStatesWhatIsMissing(t *testing.T) {
	_, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-5", map[string]string{
		"PHASE":          state.Building,
		"FAILURE_CLASS":  state.FailFaulted,
		"FAILURE_REASON": "worktree could not be created",
	})

	run, ok := srv.grillFailedRunFor(repo.Root, "COD-5")
	if !ok {
		t.Fatal("faulted run did not qualify")
	}
	if err := srv.writeGrillDossier(repo.Root, run); err != nil {
		t.Fatalf("write dossier: %v", err)
	}

	body := readFileString(t, grillDossierPath("COD-5"))
	for _, want := range []string{
		"No phase logs were recorded",
		"Not recorded for this run: Verdict, Hand-off brief, Acceptance rubric, Build notes.",
		"None — no repair pass distilled a lesson from this run.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dossier is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "WIP branch") {
		t.Errorf("dossier claimed a branch the run never cut:\n%s", body)
	}
}

// A phase log longer than the cap keeps its tail — where the failure is — and says
// it was cut, so the agent does not read a truncated line as the whole story.
func TestWriteGrillDossierTailsOversizedLog(t *testing.T) {
	_, srv, repo := grillFixServer(t)
	seedRunCheckpoint(t, srv, repo.Root, "COD-6", quarantinedCheckpoint("verify dead end"))
	huge := strings.Repeat("noise line that fills the log\n", 4000) + "the assertion that ended the run\n"
	if len(huge) <= grillDossierLogCap {
		t.Fatalf("fixture log is %d bytes, want more than the %d cap", len(huge), grillDossierLogCap)
	}
	if err := srv.stores.PhaseLogs().Upsert(repo.Root, "COD-6", "verify", huge); err != nil {
		t.Fatalf("seed phase log: %v", err)
	}

	run, _ := srv.grillFailedRunFor(repo.Root, "COD-6")
	if err := srv.writeGrillDossier(repo.Root, run); err != nil {
		t.Fatalf("write dossier: %v", err)
	}

	log := readFileString(t, filepath.Join(attachfile.Dir("COD-6"), "phase-verify.log"))
	if len(log) > grillDossierLogCap+200 {
		t.Fatalf("tailed log is %d bytes, want roughly the %d cap", len(log), grillDossierLogCap)
	}
	if !strings.HasPrefix(log, "[tailed:") {
		t.Fatalf("tailed log does not say it was cut: %q", log[:60])
	}
	if !strings.HasSuffix(log, "the assertion that ended the run\n") {
		t.Error("tailed log dropped the end of the run")
	}
	if !strings.Contains(readFileString(t, grillDossierPath("COD-6")), "tailed to its last 64KB") {
		t.Error("index does not flag the tailed log")
	}
}

// A fix session's rewrite re-describes the quarantined ticket and nothing else: the
// picker must not pick it up again before the failed attempt's branch and checkpoint
// are cleared, which only a requeue does.
func TestGrillApplyFixRewriteLeavesLabelsAlone(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	sess, err := stores.Grill().Create(hubstore.NewGrillSession{
		Repo:       root,
		IssueID:    "COD-1",
		Mode:       hubstore.GrillModeFix,
		AutoAccept: true,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	body, _ := json.Marshal(grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "The original goal, plus what the failed attempt revealed.",
		Summary:             "verify tripped on a second breakpoint",
	})
	if _, _, err := stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{
		Role:    hubstore.GrillRoleAgent,
		Kind:    hubstore.GrillKindOutcome,
		Payload: string(body),
	}); err != nil {
		t.Fatalf("append outcome: %v", err)
	}
	if _, err := stores.Grill().Transition(sess.ID, hubstore.GrillFinished, ""); err != nil {
		t.Fatalf("finish session: %v", err)
	}

	_, out := applyGrill(t, ts, sess.ID, GrillApplyRequest{})
	if !out.Applied {
		t.Fatalf("apply = %+v, want applied", out)
	}
	if len(fake.descriptions) == 0 {
		t.Fatal("the rewrite never reached the tracker")
	}
	if len(fake.labels) != 0 {
		t.Fatalf("a fix rewrite moved the quarantined ticket's labels: %+v", fake.labels)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
