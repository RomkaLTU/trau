package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/proofsbranch"
)

// titledTracker is a fakeTracker whose Title returns a fixed ticket title, so
// the summary fallback path is observable.
type titledTracker struct {
	fakeTracker
	title string
}

func (t *titledTracker) Title(context.Context, string) (string, error) { return t.title, nil }

func writeSliceVerdict(t *testing.T, id string, v verdict) {
	t.Helper()
	if err := writeVerdictFile(verifyPath(id), v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })
}

func writeSliceRubric(t *testing.T, id, content string) {
	t.Helper()
	if err := os.WriteFile(rubricPath(id), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(rubricPath(id)) })
}

func TestTicketRef(t *testing.T) {
	cases := []struct {
		provider string
		internal string
		id       string
		want     string
	}{
		{"linear", "", "COD-1", "Linear: COD-1"},
		{"linear", "LOOP", "COD-1", "Linear: COD-1"},
		{"linear", "loop", "LOOP-2", "Ref: LOOP-2"},
		{"jira", "LOOP", "TMS-9", "Jira: TMS-9"},
		{"jira", "LOOP", "LOOP-3", "Ref: LOOP-3"},
		{"internal", "LOOP", "LOOP-3", "Ref: LOOP-3"},
		{"github", "", "COD-4", "Ref: COD-4"},
		{"", "", "COD-5", "Ref: COD-5"},
	}
	for _, tc := range cases {
		p := &Pipeline{TrackerProvider: tc.provider, InternalPrefix: tc.internal}
		if got := p.ticketRef(tc.id); got != tc.want {
			t.Errorf("ticketRef(%q) with provider=%q internal=%q = %q, want %q", tc.id, tc.provider, tc.internal, got, tc.want)
		}
	}
}

// TestPRBodySkippedBrowserNeverClaimsBrowserQA: a verify that did not drive the
// browser must yield a PR body that says so — no driven claim, no pre-checked
// test plan.
func TestPRBodySkippedBrowserNeverClaimsBrowserQA(t *testing.T) {
	id := "COD-91062"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.TrackerProvider = "linear"
	p.AppURL = "http://app.test"
	writeSliceVerdict(t, id, verdict{Pass: true, Summary: "backend checks pass", Browser: "skipped", BrowserNotes: "no automation browser reachable"})

	body := p.prBody(context.Background(), id, "")

	if !strings.Contains(body, "Browser QA: not run — no automation browser reachable") {
		t.Errorf("body must state the browser was not run:\n%s", body)
	}
	if strings.Contains(body, "driven") {
		t.Errorf("body claims browser QA on a skipped run:\n%s", body)
	}
	if strings.Contains(body, "[x]") {
		t.Errorf("body carries a pre-checked claim:\n%s", body)
	}
	if !strings.Contains(body, "Linear: "+id) {
		t.Errorf("body missing tracker trailer:\n%s", body)
	}
}

func TestPRBodyDrivenBrowserNamesTarget(t *testing.T) {
	id := "COD-91063"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.AppURL = "http://app.test"
	writeSliceVerdict(t, id, verdict{Pass: true, Summary: "UI verified", Browser: "driven"})

	body := p.prBody(context.Background(), id, "")

	if !strings.Contains(body, "Browser QA: driven against http://app.test") {
		t.Errorf("driven verdict must name the target URL:\n%s", body)
	}
}

// TestPRBodyFallbackNeverEmbedsJSON: with no captured build summary the Summary
// derives from the ticket title, the Testing section reflects the rubric's tests
// and the verdict's checks, and none of the JSON artifacts leak in raw.
func TestPRBodyFallbackNeverEmbedsJSON(t *testing.T) {
	id := "COD-91064"
	p := newTestPipeline(t, fakeRunner{}, &titledTracker{title: "Add cart totals."})
	p.TrackerProvider = "jira"
	writeSliceVerdict(t, id, verdict{
		Pass:    true,
		Summary: "totals compute correctly",
		Browser: "not-applicable",
		Checks:  []checkResult{{Name: "build", Pass: true}, {Name: "tests", Pass: true}},
	})
	writeSliceRubric(t, id, `{"ticket":"`+id+`","acceptance_criteria":["totals shown"],"required_tests":["go test ./internal/cart"],"fail_conditions":[]}`)

	body := p.prBody(context.Background(), id, "")

	if !strings.Contains(body, "## Summary\nImplements "+id+": Add cart totals.") {
		t.Errorf("missing title-derived summary fallback:\n%s", body)
	}
	if !strings.Contains(body, "Tests: go test ./internal/cart — passed") {
		t.Errorf("missing required-tests line:\n%s", body)
	}
	if !strings.Contains(body, "Verify checks: build passed, tests passed") {
		t.Errorf("missing verify-checks line:\n%s", body)
	}
	if !strings.Contains(body, "Browser QA: not applicable — backend-only slice") {
		t.Errorf("missing backend-only browser line:\n%s", body)
	}
	if !strings.Contains(body, "Jira: "+id) {
		t.Errorf("jira repo must get a Jira trailer:\n%s", body)
	}
	if strings.Contains(body, `{"`) || strings.Contains(body, "Linear:") {
		t.Errorf("body leaks raw JSON or the wrong tracker:\n%s", body)
	}
}

// TestPRBodyNeverEmbedsBuildResultJSON: when the build result is the JSON object
// the agent interface invites, the Summary is the prose that object carried —
// never the flattened, mid-object-truncated blob the raw object used to yield.
func TestPRBodyNeverEmbedsBuildResultJSON(t *testing.T) {
	id := "COD-91069"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	result := `{"status":"completed","ticket":"` + id + `","branch":"feature/` + id + `-add-footer",` +
		`"summary":"Added a backoffice-wide footer with a copyright line, rendered in both the dashboard shell and the auth shell.",` +
		`"files":["apps/backoffice/src/components/app-footer.tsx (new)","apps/backoffice/src/components/auth-shell.tsx"],` +
		`"verification":{"tests":"none — the backoffice workspace has no test runner"}}`
	if err := p.State.Set(id, "BUILD_SUMMARY", summarizeBuildOutput(result)); err != nil {
		t.Fatal(err)
	}
	writeSliceVerdict(t, id, verdict{Pass: true, Summary: "footer renders", Browser: "driven"})

	body := p.prBody(context.Background(), id, "")

	if !strings.Contains(body, "## Summary\nAdded a backoffice-wide footer with a copyright line, rendered in both the dashboard shell and the auth shell.") {
		t.Errorf("summary must be the prose the result carried:\n%s", body)
	}
	for _, leak := range []string{"{", "}", `"status"`, `"files"`, "…"} {
		if strings.Contains(body, leak) {
			t.Errorf("body leaks build-result JSON (%q):\n%s", leak, body)
		}
	}
}

func TestPRBodyWithoutVerdictStatesIt(t *testing.T) {
	id := "COD-91065"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})

	body := p.prBody(context.Background(), id, "")

	if !strings.Contains(body, "No verify verdict was recorded for this run") {
		t.Errorf("missing honest no-verdict line:\n%s", body)
	}
	if !strings.Contains(body, "## Summary\nImplements "+id+".") {
		t.Errorf("missing neutral summary with no title either:\n%s", body)
	}
}

func TestPRBodiesCarryNoAttribution(t *testing.T) {
	id := "COD-91066"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.TrackerProvider = "linear"
	if err := p.State.Set(id, "BUILD_SUMMARY", "Adds per-repo config scoping. Repo settings now override user settings."); err != nil {
		t.Fatal(err)
	}
	writeSliceVerdict(t, id, verdict{Pass: true, Summary: "scoping verified", Browser: "not-applicable"})

	for name, body := range map[string]string{
		"slice": p.prBody(context.Background(), id, ""),
		"epic":  p.epicPRBody("COD-91067"),
	} {
		for _, banned := range []string{"Trau", "trau", " loop", "AI", "utomated", "[x]", "Test plan"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s body contains %q:\n%s", name, banned, body)
			}
		}
	}
	if body := p.epicPRBody("COD-91067"); !strings.Contains(body, "Linear: COD-91067") {
		t.Errorf("epic body missing tracker trailer:\n%s", body)
	}
}

func TestRenderProofsSectionPublicEmbedsInlineImages(t *testing.T) {
	pub := proofsbranch.Publication{
		Owner:  "acme",
		Repo:   "web",
		Branch: proofsbranch.Branch,
		Files: []proofsbranch.File{
			{Path: "COD-1148/proof-1.png", Caption: "cart totals"},
			{Path: "COD-1148/proof-2.png", Caption: "checkout"},
		},
	}

	got := renderProofsSection(pub, "driven against http://app.test")

	if !strings.HasPrefix(got, "### QA proofs\n") {
		t.Errorf("missing QA proofs header:\n%s", got)
	}
	if !strings.Contains(got, "Browser QA: driven against http://app.test") {
		t.Errorf("missing browser outcome line:\n%s", got)
	}
	want := "![cart totals](https://raw.githubusercontent.com/acme/web/trau-proofs/COD-1148/proof-1.png)"
	if !strings.Contains(got, want) {
		t.Errorf("public repo must embed inline images keyed by branch name:\n%s", got)
	}
	if strings.Contains(got, "github.com/acme/web/blob") {
		t.Errorf("public repo must not use blob links:\n%s", got)
	}
}

func TestRenderProofsSectionPrivateUsesLinks(t *testing.T) {
	pub := proofsbranch.Publication{
		Owner:   "acme",
		Repo:    "web",
		Branch:  proofsbranch.Branch,
		Private: true,
		Files: []proofsbranch.File{
			{Path: "COD-1148/proof-1.png", Caption: "cart totals"},
		},
	}

	got := renderProofsSection(pub, "")

	if strings.Contains(got, "![") {
		t.Errorf("private repo image proxy cannot render inline images:\n%s", got)
	}
	want := "[cart totals](https://github.com/acme/web/blob/trau-proofs/COD-1148/proof-1.png)"
	if !strings.Contains(got, want) {
		t.Errorf("private repo must emit clickable blob links:\n%s", got)
	}
	if strings.Contains(got, "Browser QA:") {
		t.Errorf("no browser outcome means no outcome line:\n%s", got)
	}
}

// TestProofsSectionAbsentWithoutProofs guards the truthful-body invariant: no
// publisher, or a publisher that pushed nothing, yields no QA section at all.
func TestProofsSectionAbsentWithoutProofs(t *testing.T) {
	id := "COD-91148"

	nilPub := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	if got := nilPub.proofsSection(context.Background(), id); got != "" {
		t.Errorf("no publisher must yield no section, got %q", got)
	}

	empty := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	empty.PublishProofs = func(context.Context, string) (proofsbranch.Publication, error) {
		return proofsbranch.Publication{}, nil
	}
	if got := empty.proofsSection(context.Background(), id); got != "" {
		t.Errorf("empty publication must yield no section, got %q", got)
	}
}

// TestProofsSectionPublishFailureIsNonFatal: a failed proofs push warns via a
// durable event and delivers the PR with no QA section rather than blocking.
func TestProofsSectionPublishFailureIsNonFatal(t *testing.T) {
	var buf bytes.Buffer
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.Events = event.New(&buf)
	p.PublishProofs = func(context.Context, string) (proofsbranch.Publication, error) {
		return proofsbranch.Publication{}, errors.New("push rejected")
	}

	if got := p.proofsSection(context.Background(), "COD-91148"); got != "" {
		t.Errorf("publish failure must yield no section, got %q", got)
	}
	evs := kindEvents(t, &buf, event.KindProofsPublishFailed)
	if len(evs) != 1 {
		t.Fatalf("emitted %d proofs_publish_failed events, want 1", len(evs))
	}
	if got := strField(evs[0].Fields, "ticket"); got != "COD-91148" {
		t.Errorf("ticket = %q, want COD-91148", got)
	}
}

// TestProofsSectionCarriesVerdictOutcome: a rendered section names the driven
// browser target from the run's verdict and lands in the assembled PR body.
func TestProofsSectionCarriesVerdictOutcome(t *testing.T) {
	id := "COD-91148"
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.AppURL = "http://app.test"
	writeSliceVerdict(t, id, verdict{Pass: true, Summary: "UI verified", Browser: "driven"})
	p.PublishProofs = func(context.Context, string) (proofsbranch.Publication, error) {
		return proofsbranch.Publication{
			Owner:  "acme",
			Repo:   "web",
			Branch: proofsbranch.Branch,
			Files:  []proofsbranch.File{{Path: id + "/proof-1.png", Caption: "home"}},
		}, nil
	}

	section := p.proofsSection(context.Background(), id)
	if !strings.Contains(section, "Browser QA: driven against http://app.test") {
		t.Errorf("section must carry the driven outcome:\n%s", section)
	}
	if body := p.prBody(context.Background(), id, section); !strings.Contains(body, "### QA proofs") {
		t.Errorf("PR body must include the QA proofs section:\n%s", body)
	}
}

func TestSummarizeBuildOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain prose", "Adds the widget. Covers the edge case.", "Adds the widget. Covers the edge case."},
		{"skips markdown structure", "# Done\n\n- touched a.go\n\nAdds the widget under settings.\n\nMore detail here.", "Adds the widget under settings."},
		{"caps at three sentences", "One. Two. Three. Four.", "One. Two. Three."},
		{"rejects loop attribution", "Implemented via the Trau loop.", ""},
		{"rejects ai attribution", "The AI implemented this ticket.", ""},
		{"structure only", "## Summary\n\n- a\n- b", ""},
		{"empty", "", ""},
		{
			"unwraps a json result to its prose",
			`{"status":"completed","ticket":"COD-7","files":["a.tsx"],"summary":"Adds a shared footer to both shells."}`,
			"Adds a shared footer to both shells.",
		},
		{
			"unwraps a pretty-printed json result",
			"{\n  \"status\": \"completed\",\n  \"summary\": \"Adds cart totals to the checkout page.\"\n}",
			"Adds cart totals to the checkout page.",
		},
		{"json result carrying no prose", `{"status":"completed","files":["a.tsx"]}`, ""},
		{"truncated json never flattens", `{"status": "completed", "summary": "Adds a foot`, ""},
		{"json array never flattens", `[{"summary":"Adds a shared footer."}]`, ""},
		{"json prose still screened for attribution", `{"summary":"The agent implemented this ticket."}`, ""},
	}
	for _, tc := range cases {
		if got := summarizeBuildOutput(tc.in); got != tc.want {
			t.Errorf("%s: summarizeBuildOutput = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestBuildCapturesSummary: the build phase stores its output's leading prose in
// the checkpoint so a later CommitAndPR — possibly in another process — builds
// the PR summary from what the build actually reported.
func TestBuildCapturesSummary(t *testing.T) {
	id := "COD-91068"
	p := newTestPipeline(t, refusalRunner{buildFinal: "Adds truthful PR bodies. The trailer now follows the tracker.\n\nDetails below."}, &fakeTracker{})
	p.Git = guardGit{dirty: true}

	if err := p.Build(context.Background(), id); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := p.State.Get(id, "BUILD_SUMMARY"), "Adds truthful PR bodies. The trailer now follows the tracker."; got != want {
		t.Errorf("BUILD_SUMMARY = %q, want %q", got, want)
	}
}
