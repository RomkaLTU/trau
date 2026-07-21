package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
)

// filesGit is a worktreeLister returning a fixed changed-file set so the UI
// classifier and browser gate can be driven deterministically.
type filesGit struct {
	fakeGit
	files []string
}

func (g filesGit) WorktreeChangedFiles(context.Context, string) ([]string, error) {
	return g.files, nil
}

// verdictRunner writes a fixed verdict to the verify path on each call, standing
// in for the re-verify agent so the always-mode gate can observe a driven or
// still-undriven outcome. It counts calls to assert the single re-verify.
type verdictRunner struct {
	path  string
	v     verdict
	calls int
}

func (r *verdictRunner) Run(context.Context, string, string) (agent.Result, error) {
	r.calls++
	data, _ := json.Marshal(r.v)
	_ = os.WriteFile(r.path, data, 0o644)
	return agent.Result{}, nil
}

var uiFiles = []string{"apps/web/src/App.tsx"}
var backendFiles = []string{"internal/pipeline/pipeline.go"}

func TestIsUIFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"apps/web/src/App.tsx", true},
		{"src/Widget.jsx", true},
		{"components/Card.vue", true},
		{"routes/+page.svelte", true},
		{"resources/views/dashboard.blade.php", true},
		{"styles/main.css", true},
		{"theme/tokens.scss", true},
		{"app/templates/index.html", true},
		{"server/views/email.txt", true},
		{"pkg\\ui\\Button.tsx", true},
		{"internal/pipeline/pipeline.go", false},
		{"cmd/trau/main.go", false},
		{"api/routes.ts", false},
		{"README.md", false},
		{"db/migrations/001_init.sql", false},
		{"config/app.php", false},
	}
	for _, tc := range cases {
		if got := isUIFile(tc.path); got != tc.want {
			t.Errorf("isUIFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestBrowserOutcomeAccounting pins the accounting normalization, including the
// back-compat rule: a verdict with no browser field (old runs) reads as skipped.
func TestBrowserOutcomeAccounting(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"pass":true,"summary":"ok","failures":[]}`, "skipped"},
		{`{"pass":true,"browser":"driven"}`, "driven"},
		{`{"pass":true,"browser":"not-applicable"}`, "not-applicable"},
		{`{"pass":true,"browser":"skipped","browser_notes":"cannot reach APP_URL"}`, "skipped"},
		{`{"pass":true,"browser":"nonsense"}`, "skipped"},
	}
	for _, tc := range cases {
		var v verdict
		if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
			t.Fatalf("unmarshal %q: %v", tc.raw, err)
		}
		if got := browserOutcome(v); got != tc.want {
			t.Errorf("browserOutcome(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestBrowserNoteEmptyWithoutAppURL(t *testing.T) {
	for _, mode := range []string{"auto", "always", "never"} {
		if got := browserNote(mode, ""); got != "" {
			t.Errorf("browserNote(%q, \"\") = %q, want empty (no APP_URL target)", mode, got)
		}
	}
	if got := browserNote("auto", "http://localhost:3000"); got == "" {
		t.Error("browserNote(auto, url) must be non-empty when an APP_URL is configured")
	}
}

// TestGateBrowserVerifyAdvisory covers every gate combination that stays advisory
// (no re-run, no pause): the full {auto, always} × {driven, skipped,
// not-applicable, missing} × {UI, backend} matrix minus the always+UI+undriven
// cells that re-verify. A violation emits exactly one verify_no_browser event.
func TestGateBrowserVerifyAdvisory(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		browser    string
		ui         bool
		appURL     string
		wantEvents int
	}{
		{"auto backend skipped", "auto", "skipped", false, "http://localhost:3000", 0},
		{"auto backend missing", "auto", "", false, "http://localhost:3000", 0},
		{"always backend not-applicable", "always", "not-applicable", false, "http://localhost:3000", 0},
		{"auto ui driven", "auto", "driven", true, "http://localhost:3000", 0},
		{"always ui driven", "always", "driven", true, "http://localhost:3000", 0},
		{"auto ui skipped", "auto", "skipped", true, "http://localhost:3000", 1},
		{"auto ui not-applicable", "auto", "not-applicable", true, "http://localhost:3000", 1},
		{"auto ui missing", "auto", "", true, "http://localhost:3000", 1},
		{"always ui skipped no app url", "always", "skipped", true, "", 1},
		{"always ui missing no app url", "always", "", true, "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			files := backendFiles
			if tc.ui {
				files = uiFiles
			}
			runner := &verdictRunner{}
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.Git = filesGit{files: files}
			p.Events = event.New(&buf)
			p.BrowserVerify = tc.mode
			p.AppURL = tc.appURL

			v := verdict{Pass: true, Browser: tc.browser}
			if err := p.gateBrowserVerify(context.Background(), "COD-1", v, "", "", "", "", "", ""); err != nil {
				t.Fatalf("gate returned %v, want nil (advisory)", err)
			}
			if runner.calls != 0 {
				t.Errorf("re-verify ran %d times, want 0 (advisory path)", runner.calls)
			}
			evs := kindEvents(t, &buf, event.KindVerifyNoBrowser)
			if len(evs) != tc.wantEvents {
				t.Fatalf("emitted %d verify_no_browser events, want %d", len(evs), tc.wantEvents)
			}
			if tc.wantEvents == 1 {
				if got := strField(evs[0].Fields, "ticket"); got != "COD-1" {
					t.Errorf("event ticket = %q, want COD-1", got)
				}
			}
		})
	}
}

// TestGateBrowserVerifyAlwaysReVerifies covers the always+UI+undriven cells: a
// single re-verify with the must-drive instruction. When it drives, the run
// proceeds; when it stays undriven, the ticket pauses blamelessly with a reason
// carrying the verdict's browser_notes.
func TestGateBrowserVerifyAlwaysReVerifies(t *testing.T) {
	t.Run("re-verify drives then proceeds", func(t *testing.T) {
		id := "COD-2"
		var buf bytes.Buffer
		runner := &verdictRunner{path: verifyPath(id), v: verdict{Pass: true, Browser: "driven", BrowserNotes: "exercised the dashboard"}}
		t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })
		p := newTestPipeline(t, runner, &fakeTracker{})
		p.Git = filesGit{files: uiFiles}
		p.Events = event.New(&buf)
		p.BrowserVerify = "always"
		p.AppURL = "http://localhost:3000"

		v := verdict{Pass: true, Browser: "skipped"}
		if err := p.gateBrowserVerify(context.Background(), id, v, "", "", "", "", "", ""); err != nil {
			t.Fatalf("gate returned %v, want nil after the browser was driven on re-verify", err)
		}
		if runner.calls != 1 {
			t.Errorf("re-verify ran %d times, want exactly 1", runner.calls)
		}
		if evs := kindEvents(t, &buf, event.KindVerifyNoBrowser); len(evs) != 0 {
			t.Errorf("emitted %d verify_no_browser events, want 0 (re-verify satisfied the gate)", len(evs))
		}
	})

	// Every undriven initial value (skipped, not-applicable, missing) on a UI
	// slice under `always` takes the single re-verify; when it stays undriven the
	// ticket pauses blamelessly, never quarantines, and carries browser_notes.
	for _, initial := range []string{"skipped", "not-applicable", ""} {
		name := initial
		if name == "" {
			name = "missing"
		}
		t.Run("still undriven pauses ("+name+")", func(t *testing.T) {
			id := "COD-3" + name
			runner := &verdictRunner{path: verifyPath(id), v: verdict{Pass: true, Browser: "skipped", BrowserNotes: "cannot reach APP_URL"}}
			t.Cleanup(func() { _ = os.Remove(verifyPath(id)) })
			tr := &fakeTracker{}
			p := newTestPipeline(t, runner, tr)
			p.Git = filesGit{files: uiFiles}
			p.BrowserVerify = "always"
			p.AppURL = "http://localhost:3000"

			v := verdict{Pass: true, Browser: initial}
			err := p.gateBrowserVerify(context.Background(), id, v, "", "", "", "", "", "")

			if !IsPaused(err) {
				t.Fatalf("gate returned %v, want a *PausedError", err)
			}
			if runner.calls != 1 {
				t.Errorf("re-verify ran %d times, want exactly 1", runner.calls)
			}
			pe := AsPaused(err)
			if !bytes.Contains([]byte(pe.Reason), []byte("cannot reach APP_URL")) {
				t.Errorf("pause reason %q must contain the verdict's browser_notes", pe.Reason)
			}
			if got := p.State.Get(id, "FAILURE_REASON"); !bytes.Contains([]byte(got), []byte("cannot reach APP_URL")) {
				t.Errorf("FAILURE_REASON %q must carry the browser_notes", got)
			}
			if tr.quarantineCalls != 0 || tr.fileBugCalls != 0 {
				t.Errorf("a browser skip must not quarantine (%d) or file a bug (%d)", tr.quarantineCalls, tr.fileBugCalls)
			}
		})
	}
}
