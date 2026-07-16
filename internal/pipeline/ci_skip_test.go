package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		RequireCI: true,
		RepoRoot:  root,
		GitHub:    &epicGitHub{},
		Sleep:     func(time.Duration) {},
	}
}

// A repo whose PR workflows are all path-scoped legitimately produces a PR with
// zero checks when the slice touches none of the filtered paths. That reads as
// a skip, not a CI timeout that quarantines the ticket.
func TestPollCISkipsZeroChecksWhenAllPRWorkflowsPathFiltered(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	if err := p.pollCI(context.Background(), "https://github.test/pr/1"); err != nil {
		t.Fatalf("pollCI = %v, want nil (skip)", err)
	}
}

func TestPollCITimesOutZeroChecksWhenPRWorkflowUnfiltered(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n")
	if err := p.pollCI(context.Background(), "https://github.test/pr/1"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

// EXPECTED_CHECKS is an explicit promise that named checks appear on every PR;
// a path-filtered repo does not soften that gate.
func TestPollCIExpectedChecksStillTimeOutOnPathFilteredRepo(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	p.ExpectedChecks = "build"
	if err := p.pollCI(context.Background(), "https://github.test/pr/1"); !errors.Is(err, ErrCITimeout) {
		t.Fatalf("pollCI = %v, want ErrCITimeout", err)
	}
}

func TestPollCIFailingCheckStillFailsOnPathFilteredRepo(t *testing.T) {
	p := ciTestPipeline(t, "on:\n  pull_request:\n    paths:\n      - 'web/**'\n")
	p.GitHub = &epicGitHub{checks: []Check{{Name: "ci/test", Bucket: "fail"}}}
	if err := p.pollCI(context.Background(), "https://github.test/pr/1"); !errors.Is(err, ErrCIFailed) {
		t.Fatalf("pollCI = %v, want ErrCIFailed", err)
	}
}
