package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
)

const ciTestPR = "https://github.test/pr/1"

func writeWorkflow(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ciTestPipeline(t *testing.T, workflow string) *Pipeline {
	t.Helper()
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", workflow)
	return &Pipeline{
		RequireCI: config.CIGateAuto,
		RepoRoot:  root,
		Delivery:  &epicGitHub{},
		Sleep:     func(time.Duration) {},
	}
}

// lateCheckGitHub reports a check only from the after+1'th poll on, the way CI
// hosted outside Actions posts its first one well after the PR opens.
type lateCheckGitHub struct {
	epicGitHub
	after int
	polls int
}

func (g *lateCheckGitHub) Checks(context.Context, string) ([]Check, error) {
	g.polls++
	if g.polls <= g.after {
		return nil, nil
	}
	return []Check{{Name: "buildkite", Bucket: "fail"}}, nil
}

// A repo whose PR workflows are all path-scoped legitimately produces a PR with
// zero checks when the slice touches none of the filtered paths. That reads as
// a skip, not a CI timeout that quarantines the ticket.
func TestPollCISkipsZeroChecksWhenAllPRWorkflowsPathFiltered(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	if err := p.pollCI(context.Background(), ciTestPR, "main"); err != nil {
		t.Fatalf("pollCI = %v, want nil (skip)", err)
	}
}

func TestPollCITimesOutZeroChecksWhenPRWorkflowUnfiltered(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n")
	if err := p.pollCI(context.Background(), ciTestPR, "main"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

// EXPECTED_CHECKS is an explicit promise that named checks appear on every PR;
// a path-filtered repo does not soften that gate.
func TestPollCIExpectedChecksStillTimeOutOnPathFilteredRepo(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	p.ExpectedChecks = "build"
	if err := p.pollCI(context.Background(), ciTestPR, "main"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

func TestPollCIFailingCheckStillFailsOnPathFilteredRepo(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	p.Delivery = &epicGitHub{checks: []Check{{Name: "ci/test", Bucket: "fail"}}}
	if err := p.pollCI(context.Background(), ciTestPR, "main"); !errors.Is(err, ErrCIFailed) {
		t.Fatalf("pollCI = %v, want ErrCIFailed", err)
	}
}

// A base no pull_request workflow targets — an epic base, a release branch, any
// base a repo scopes its triggers away from — can never receive a check. Under
// auto that is a warning and a merge, not the timeout-then-quarantine a verified
// PR used to end in.
func TestPollCIAutoMergesWhenNoWorkflowTargetsBase(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    branches: [main]\n")
	if err := p.pollCI(context.Background(), ciTestPR, "epic/COD-1-checkout"); err != nil {
		t.Fatalf("pollCI = %v, want nil (merge with a warning)", err)
	}
}

// A repo with only a deploy-on-push workflow has no PR CI at all.
func TestPollCIAutoMergesWhenRepoHasNoPRWorkflow(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  push:\n    branches: [main]\n")
	if err := p.pollCI(context.Background(), ciTestPR, "main"); err != nil {
		t.Fatalf("pollCI = %v, want nil (merge with a warning)", err)
	}
}

// REQUIRE_CI=1 is the explicit "wait for checks whatever the repo looks like"
// mode: absent checks still run out the clock.
func TestPollCIStrictTimesOutWhenNoWorkflowTargetsBase(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    branches: [main]\n")
	p.RequireCI = config.CIGateOn
	if err := p.pollCI(context.Background(), ciTestPR, "epic/COD-1-checkout"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

func TestPollCIExpectedChecksStillTimeOutWhenNoWorkflowTargetsBase(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    branches: [main]\n")
	p.ExpectedChecks = "build"
	if err := p.pollCI(context.Background(), ciTestPR, "epic/COD-1-checkout"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

// The waiver is for PRs that receive no check at all. Checks that did appear
// decide the gate exactly as before, whatever the workflow scan says about the
// base — CI hosted outside GitHub Actions reports this way.
func TestPollCIAutoJudgesChecksThatAppearOnBaseWithNoWorkflow(t *testing.T) {
	cases := []struct {
		bucket string
		want   error
	}{
		{bucket: "fail", want: ErrCIFailed},
		{bucket: "pending", want: ErrCITimeout},
	}
	for _, tc := range cases {
		t.Run(tc.bucket, func(t *testing.T) {
			p := ciTestPipeline(t, "on:\n  pull_request:\n    branches: [main]\n")
			p.Delivery = &epicGitHub{checks: []Check{{Name: "ci/test", Bucket: tc.bucket}}}
			if err := p.pollCI(context.Background(), ciTestPR, "epic/COD-1-checkout"); !errors.Is(err, tc.want) {
				t.Fatalf("pollCI = %v, want %v", err, tc.want)
			}
		})
	}
}

// A repo with no pull_request workflow proves nothing about CI hosted outside
// Actions, so its first check gets the full CI_TIMEOUT to arrive rather than the
// grace window: a slow check must still be judged, not merged past.
func TestPollCIAutoWaitsForLateCheckWhenRepoHasNoPRWorkflow(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  push:\n    branches: [main]\n")
	p.CITimeout, p.CIPoll = 600, 30
	p.Delivery = &lateCheckGitHub{after: 6}
	if err := p.pollCI(context.Background(), ciTestPR, "main"); !errors.Is(err, ErrCIFailed) {
		t.Fatalf("pollCI = %v, want ErrCIFailed", err)
	}
}

// The waiver for a base the repo's PR workflows skip lands one grace window in,
// not one CI_TIMEOUT in: the gate used to burn the full ten minutes before
// reaching the same conclusion.
func TestPollCIWaivesChecklessPRAfterGraceNotTimeout(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    branches: [main]\n")
	p.CITimeout, p.CIPoll = 600, 30
	polls := 0
	p.Sleep = func(time.Duration) { polls++ }

	if err := p.pollCI(context.Background(), ciTestPR, "epic/COD-1-checkout"); err != nil {
		t.Fatalf("pollCI = %v, want nil (merge with a warning)", err)
	}
	if want := noChecksGrace / p.CIPoll; polls != want {
		t.Fatalf("waited %d polls before waiving the gate, want %d", polls, want)
	}
}
