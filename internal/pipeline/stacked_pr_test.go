package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// stackGitHub answers the stack-membership question the auto-merge path asks, and
// records the PR of every question, so a test can also prove the question was never
// asked.
type stackGitHub struct {
	mergeGitHub
	stacked bool
	err     error
	asked   []string
}

func (g *stackGitHub) InStack(_ context.Context, pr string) (bool, error) {
	g.asked = append(g.asked, pr)
	return g.stacked, g.err
}

// A human can stack Trau's PRs at any moment, and GitHub's legacy merge endpoint
// then refuses to merge the layer at all. Trau must not walk into that refusal: the
// ticket is parked for a human, with a reason that names the PR and both ways out.
func TestCIAndMergeGivesUpOnAStackedPR(t *testing.T) {
	id := "COD-90420"
	git := &mergeGit{branch: "feature/COD-90420-x"}
	gh := &stackGitHub{stacked: true}
	tr := &fakeTracker{}
	p := newMergePipeline(t, git, gh, tr)
	seedPROpen(t, p, id, "412", git.branch)

	err := p.CIAndMerge(context.Background(), id)
	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("CIAndMerge = %v, want a *GiveUpError", err)
	}
	for _, want := range []string{"https://x/pr/412", "part of a GitHub stack", "gh stack merge", "remove it from the stack and requeue"} {
		if !strings.Contains(g.Reason, want) {
			t.Errorf("give-up reason = %q, want it to name %q", g.Reason, want)
		}
	}
	if gh.mergeCalls != 0 {
		t.Errorf("Merge called %d times, want 0 — the endpoint would refuse it", gh.mergeCalls)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine called %d times, want 1 (parked for a human)", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
}

// The check may never cost a delivery that would have worked: an unstacked PR merges
// exactly as before, and so does one whose stack membership gh cannot report.
func TestCIAndMergeMergesWhenThePRIsNotKnownToBeStacked(t *testing.T) {
	tests := []struct {
		name string
		gh   *stackGitHub
	}{
		{"unstacked PR", &stackGitHub{}},
		{"detection failed", &stackGitHub{err: errors.New("gh api pulls/412: HTTP 404: Not Found")}},
		{"detection failed and claimed stacked", &stackGitHub{stacked: true, err: errors.New("gh api pulls/412: parse: unexpected end of JSON input")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "COD-90421"
			git := &mergeGit{branch: "feature/COD-90421-x"}
			tr := &fakeTracker{}
			p := newMergePipeline(t, git, tt.gh, tr)
			seedPROpen(t, p, id, "412", git.branch)

			if err := p.CIAndMerge(context.Background(), id); err != nil {
				t.Fatalf("CIAndMerge = %v, want nil", err)
			}
			if tt.gh.mergeCalls != 1 {
				t.Errorf("Merge called %d times, want 1", tt.gh.mergeCalls)
			}
			if tr.quarantineCalls != 0 {
				t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
			}
			if len(tt.gh.asked) != 1 || tt.gh.asked[0] != "412" {
				t.Errorf("InStack asked about %v, want [412]", tt.gh.asked)
			}
		})
	}
}

// stackWaitGitHub is the AUTO_MERGE=0 wait with a stack answer nobody may ask for.
type stackWaitGitHub struct {
	waitGitHub
	asked int
}

func (g *stackWaitGitHub) InStack(context.Context, string) (bool, error) {
	g.asked++
	return true, nil
}

// With AUTO_MERGE=0 the human merges the PR, and a human can merge a whole stack, so
// the wait for their merge is the right behavior for a stacked PR too — the check
// guards the auto-merge alone and must not fire here.
func TestCIAndMergeLeavesAStackedPRToTheOperator(t *testing.T) {
	id := "COD-90422"
	gh := &stackWaitGitHub{waitGitHub: waitGitHub{replies: []prReply{{state: "OPEN"}, {state: "MERGED"}}}}
	tr := &fakeTracker{}
	p := newWaitPipeline(t, gh, tr)
	seedPROpen(t, p, id, "412", "feature/COD-90422-x")

	if err := p.CIAndMerge(context.Background(), id); err != nil {
		t.Fatalf("CIAndMerge = %v, want nil", err)
	}
	if gh.asked != 0 {
		t.Errorf("InStack called %d times, want 0 — the operator owns this merge", gh.asked)
	}
	if tr.quarantineCalls != 0 {
		t.Errorf("Quarantine called %d times, want 0", tr.quarantineCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Merged {
		t.Errorf("PHASE = %q, want merged once the operator merged it", got)
	}
}

// The REST PR payload carries a top-level "stack" object only for a layer of a
// stack. Every other shape — the key missing on a repo the preview has not reached,
// a payload gh could not produce — must read as unstacked, because that answer keeps
// today's merge path.
func TestParseStackMembership(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		want    bool
		wantErr bool
	}{
		{"stacked PR", `{"number":5,"stack":{"base":{"ref":"main","sha":"e864f25"},"id":171306,"number":8,"position":1,"size":3}}`, true, false},
		{"unstacked PR omits the key", `{"number":13,"base":{"ref":"main"}}`, false, false},
		{"merged layer keeps its stack", `{"number":1,"stack":{"base":{"ref":"main","sha":null},"id":171306,"number":8,"position":1,"size":3}}`, true, false},
		{"key present but null", `{"number":13,"stack":null}`, false, false},
		{"empty output", ``, false, true},
		{"malformed payload", `gh: Not Found (HTTP 404)`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStackMembership(tc.out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseStackMembership(%q) error = %v, want error %v", tc.out, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseStackMembership(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
