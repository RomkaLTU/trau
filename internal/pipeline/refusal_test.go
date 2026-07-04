package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

// refusalRunner answers the build phase with a canned final message and every
// other phase with an empty success, so a Process run can be driven up to and
// through the refusal check.
type refusalRunner struct {
	buildFinal string
}

func (r refusalRunner) Run(ctx context.Context, prompt, label string) (agent.Result, error) {
	if strings.HasPrefix(label, "build") {
		return agent.Result{Final: r.buildFinal}, nil
	}
	return agent.Result{}, nil
}

// resetTracker counts Reset calls so the refusal cleanup path is observable.
type resetTracker struct {
	fakeTracker
	resetCalls int
}

func (t *resetTracker) Reset(context.Context, string) error {
	t.resetCalls++
	return nil
}

// TestBuildRefusalResetsTicket: a build agent that replies with the REFUSED
// sentinel over an untouched worktree must surface a *RefusedError, and the
// handler must reset the ticket — checkpoint cleared and tracker restored — so
// nothing half-started lingers in a repo the ticket does not belong to.
func TestBuildRefusalResetsTicket(t *testing.T) {
	id := "COD-90158"
	tr := &resetTracker{}
	p := newTestPipeline(t, refusalRunner{buildFinal: "I looked around.\nREFUSED: this ticket targets the salonradar.com Laravel app"}, tr)
	p.Git = guardGit{dirty: false}
	p.RequireRepoChanges = true

	err := p.Process(context.Background(), id)

	r := AsRefused(err)
	if r == nil {
		t.Fatalf("Process error = %v, want *RefusedError", err)
	}
	if want := "this ticket targets the salonradar.com Laravel app"; r.Reason != want {
		t.Errorf("refusal reason = %q, want %q", r.Reason, want)
	}
	if phase := p.State.Get(id, "PHASE"); phase != "" {
		t.Errorf("PHASE = %q after refusal, want cleared state", phase)
	}
	if tr.resetCalls != 1 {
		t.Errorf("tracker Reset calls = %d, want 1", tr.resetCalls)
	}
	if tr.quarantineCalls != 0 || tr.fileBugCalls != 0 {
		t.Errorf("refusal must not quarantine or file a bug (quarantine=%d, bugs=%d)", tr.quarantineCalls, tr.fileBugCalls)
	}
}

// TestBuildRefusalIgnoredWhenDirty: a REFUSED sentinel contradicted by actual
// changes in the worktree is ignored — the changes win and the run proceeds
// past the refusal check (and past the repo-change guard, which sees the diff).
func TestBuildRefusalIgnoredWhenDirty(t *testing.T) {
	id := "COD-90159"
	tr := &resetTracker{}
	p := newTestPipeline(t, refusalRunner{buildFinal: "REFUSED: wrong repo"}, tr)
	p.Git = guardGit{dirty: true}
	p.RequireRepoChanges = true

	err := p.Process(context.Background(), id)

	if AsRefused(err) != nil {
		t.Fatalf("refusal honored despite a dirty worktree: %v", err)
	}
	if tr.resetCalls != 0 {
		t.Errorf("tracker Reset calls = %d, want 0", tr.resetCalls)
	}
	if phase := p.State.Get(id, "PHASE"); phase == "" || phase == "building" {
		t.Errorf("PHASE = %q, want progress past build", phase)
	}
}

// TestGuardFaultLeavesBuilding: when the repo-change guard trips, the ticket
// must be left at the building checkpoint — labeled a build failure and resumed
// back INTO build — never at built, where a resume would march an empty branch
// into handoff (the COD-158 trap).
func TestGuardFaultLeavesBuilding(t *testing.T) {
	id := "COD-90160"
	p := newTestPipeline(t, refusalRunner{buildFinal: "done, honest"}, &fakeTracker{})
	p.Git = guardGit{dirty: false}
	p.RequireRepoChanges = true

	err := p.Process(context.Background(), id)

	f := AsFault(err)
	if f == nil {
		t.Fatalf("Process error = %v, want *FaultError", err)
	}
	if phase := p.State.Get(id, "PHASE"); phase != "building" {
		t.Errorf("PHASE = %q after guard fault, want building", phase)
	}
	reason := p.State.Get(id, "FAILURE_REASON")
	if !strings.Contains(reason, "during build") {
		t.Errorf("FAILURE_REASON = %q, want the fault labeled 'during build'", reason)
	}
}

// TestParseRefusal pins the sentinel grammar: line-anchored, last match wins,
// prose mentions don't trip it.
func TestParseRefusal(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		want   string
		wantOK bool
	}{
		{name: "plain", out: "REFUSED: wrong repo", want: "wrong repo", wantOK: true},
		{name: "final line after prose", out: "searched everywhere\nREFUSED: targets another codebase", want: "targets another codebase", wantOK: true},
		{name: "indented", out: "  REFUSED: over there", want: "over there", wantOK: true},
		{name: "prose mention only", out: "the request was REFUSED: by the server", wantOK: false},
		{name: "lowercase", out: "refused: nope", wantOK: false},
		{name: "empty", out: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRefusal(tc.out)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseRefusal(%q) = (%q, %v), want (%q, %v)", tc.out, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
