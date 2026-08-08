package pipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

// appRunPipeline is a run isolated in a worktree, with a UI diff and an active
// browser gate — the exact shape that makes the hub's per-worktree app the thing
// verify should drive.
func appRunPipeline(t *testing.T, ensure func(context.Context, string) (string, bool, error)) *Pipeline {
	t.Helper()
	return &Pipeline{
		RepoRoot:      t.TempDir(),
		WorkTree:      t.TempDir(),
		Worktrees:     true,
		BrowserVerify: "always",
		AppURL:        "http://localhost:3000",
		AppURLs:       map[string]string{"apps/web": "http://localhost:3001"},
		Git:           filesGit{files: uiFiles},
		EnsureAppURL:  ensure,
	}
}

// TestWorktreeAppURLOverridesTheConfiguredTargets is the slice's reason to exist:
// with a tree of its own, verify drives the app that tree serves, not the dev
// server on the registered checkout, which is showing another branch entirely.
func TestWorktreeAppURLOverridesTheConfiguredTargets(t *testing.T) {
	asked := []string{}
	p := appRunPipeline(t, func(_ context.Context, ticket string) (string, bool, error) {
		asked = append(asked, ticket)
		return "http://localhost:4300", true, nil
	})

	p.adoptWorktreeAppURL(context.Background(), "COD-1584")

	if p.AppURL != "http://localhost:4300" || len(p.AppURLs) != 0 {
		t.Fatalf("targets = %q/%v, want the worktree's app alone", p.AppURL, p.AppURLs)
	}
	if got := p.sliceAppURL(context.Background()); got != "http://localhost:4300" {
		t.Errorf("sliceAppURL() = %q, want the worktree's app", got)
	}
	if got := browserNote(p.BrowserVerify, p.sliceAppURL(context.Background())); got == "" {
		t.Error("browserNote is empty, want the drive instruction naming the worktree's app")
	}
	if len(asked) != 1 || asked[0] != "COD-1584" {
		t.Fatalf("asked = %v, want one ask for COD-1584", asked)
	}

	// The app is started at first need and exactly once per run.
	p.adoptWorktreeAppURL(context.Background(), "COD-1584")
	if len(asked) != 1 {
		t.Errorf("asked %d times, want the hub asked once per run", len(asked))
	}
}

// TestWorktreeAppURLAsksUnderTheEpicsOwnKey: an epic's children share one tree,
// keyed by the epic, so the app is asked for under that key rather than the child's.
func TestWorktreeAppURLAsksUnderTheEpicsOwnKey(t *testing.T) {
	var asked string
	p := appRunPipeline(t, func(_ context.Context, ticket string) (string, bool, error) {
		asked = ticket
		return "http://localhost:4300", true, nil
	})
	p.EpicID = "COD-1500"
	// The epic branch is already resolved, so the slice's diff is listed against it
	// without the epic machinery this test has no interest in.
	p.exit.epicBranch = "epic/COD-1500-shared-tree"

	p.adoptWorktreeAppURL(context.Background(), "COD-1584")

	if asked != "COD-1500" {
		t.Fatalf("asked for %q, want the epic's own key COD-1500", asked)
	}
}

// TestWorktreeAppFailureFallsBackToTheAdvisoryPath: an app that will not boot must
// never fail the run, and must never silently fall back to the human's dev server —
// the targets are cleared so the browser gate goes advisory and the verdict says the
// UI was not driven.
func TestWorktreeAppFailureFallsBackToTheAdvisoryPath(t *testing.T) {
	p := appRunPipeline(t, func(context.Context, string) (string, bool, error) {
		return "", true, errors.New("app never listened on port 4300")
	})
	var buf bytes.Buffer
	p.Events = event.New(&buf)

	p.adoptWorktreeAppURL(context.Background(), "COD-1584")

	if p.AppURL != "" || len(p.AppURLs) != 0 {
		t.Fatalf("targets = %q/%v, want none — the configured dev server is the wrong branch", p.AppURL, p.AppURLs)
	}
	if got := browserNote(p.BrowserVerify, p.sliceAppURL(context.Background())); got != "" {
		t.Errorf("browserNote = %q, want none so the gate goes advisory", got)
	}
	if !strings.Contains(buf.String(), event.KindWorktreeAppFailed) {
		t.Errorf("events = %s, want a %s event", buf.String(), event.KindWorktreeAppFailed)
	}
}

// TestWorktreeAppLeavesAReposConfiguredTargetsAloneWhenNothingIsServed is the
// APP_START_CMD=empty case, which must behave exactly as it did before this slice.
func TestWorktreeAppLeavesAReposConfiguredTargetsAloneWhenNothingIsServed(t *testing.T) {
	p := appRunPipeline(t, func(context.Context, string) (string, bool, error) {
		return "", false, nil
	})

	p.adoptWorktreeAppURL(context.Background(), "COD-1584")

	if p.AppURL != "http://localhost:3000" || len(p.AppURLs) != 1 {
		t.Fatalf("targets = %q/%v, want the configured ones untouched", p.AppURL, p.AppURLs)
	}
}

// TestWorktreeAppIsNotStartedWhereItWouldNotBeDriven: the app starts at first
// need, so a run with no tree, a disabled browser gate, or a backend-only diff
// never asks the hub to boot one.
func TestWorktreeAppIsNotStartedWhereItWouldNotBeDriven(t *testing.T) {
	cases := []struct {
		name string
		bend func(*Pipeline)
	}{
		{"no worktrees", func(p *Pipeline) { p.Worktrees = false }},
		{"no tree yet", func(p *Pipeline) { p.WorkTree = "" }},
		{"browser gate off", func(p *Pipeline) { p.BrowserVerify = "never" }},
		{"backend slice", func(p *Pipeline) { p.Git = filesGit{files: backendFiles} }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			asked := 0
			p := appRunPipeline(t, func(context.Context, string) (string, bool, error) {
				asked++
				return "http://localhost:4300", true, nil
			})
			tt.bend(p)

			p.adoptWorktreeAppURL(context.Background(), "COD-1584")

			if asked != 0 {
				t.Errorf("asked the hub %d times, want none", asked)
			}
			if p.AppURL != "http://localhost:3000" {
				t.Errorf("AppURL = %q, want the configured target untouched", p.AppURL)
			}
		})
	}
}

// TestRosterAppURLIDFallsBackToTheDefaultEntryForAWorktreeURL: a worktree URL is
// trau's own localhost address on an allocated port, so no stored app_urls row will
// ever hold it. The roster still has to resolve, so the repo's default entry stands
// in — otherwise a repo loses its whole QA roster the moment it serves per worktree.
func TestRosterAppURLIDFallsBackToTheDefaultEntryForAWorktreeURL(t *testing.T) {
	entries := []hubclient.AppURL{
		{ID: 7, URL: "https://app.example.com"},
		{ID: 8, URL: "https://admin.example.com", Workspace: "apps/admin"},
	}
	p := &Pipeline{worktreeApp: "http://localhost:4300"}

	if got := p.rosterAppURLID(entries, "http://localhost:4300"); got != 7 {
		t.Errorf("rosterAppURLID for the worktree URL = %d, want the default entry 7", got)
	}
	if got := p.rosterAppURLID(entries, "https://admin.example.com"); got != 8 {
		t.Errorf("rosterAppURLID for a stored URL = %d, want its own entry 8", got)
	}
	// A URL that is neither stored nor this run's worktree app resolves to nothing,
	// exactly as before.
	if got := p.rosterAppURLID(entries, "https://elsewhere.example.com"); got != 0 {
		t.Errorf("rosterAppURLID for an unknown URL = %d, want 0", got)
	}
	// A repo storing only workspace entries has no default to stand in.
	if got := p.rosterAppURLID(entries[1:], "http://localhost:4300"); got != 0 {
		t.Errorf("rosterAppURLID with no default entry = %d, want 0", got)
	}
}
