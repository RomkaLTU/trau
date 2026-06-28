package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGitignore(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	return string(data)
}

func TestEnsureGitignoreCreatesWhenMissing(t *testing.T) {
	repo := t.TempDir()
	if err := EnsureGitignore(repo, ".trau/runs"); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	got := readGitignore(t, repo)
	if !strings.Contains(got, ".trau/runs/") {
		t.Errorf("missing runs dir rule; got:\n%s", got)
	}
	if !strings.Contains(got, ".trau.ini") {
		t.Errorf("missing .trau.ini rule; got:\n%s", got)
	}
	if strings.Contains(got, ".trau/checks") {
		t.Errorf(".trau/checks must never be ignored; got:\n%s", got)
	}
	// The blanket ".trau/" rule would swallow .trau/checks — must not appear.
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == ".trau/" || strings.TrimSpace(line) == ".trau" {
			t.Errorf("must not blanket-ignore .trau; got line %q", line)
		}
	}
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	repo := t.TempDir()
	if err := EnsureGitignore(repo, ".trau/runs"); err != nil {
		t.Fatalf("first EnsureGitignore: %v", err)
	}
	first := readGitignore(t, repo)
	if err := EnsureGitignore(repo, ".trau/runs"); err != nil {
		t.Fatalf("second EnsureGitignore: %v", err)
	}
	second := readGitignore(t, repo)
	if first != second {
		t.Errorf("second call mutated .gitignore:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if n := strings.Count(second, ".trau/runs/"); n != 1 {
		t.Errorf("runs rule appears %d times, want 1:\n%s", n, second)
	}
	if n := strings.Count(second, ".trau.ini"); n != 1 {
		t.Errorf(".trau.ini rule appears %d times, want 1:\n%s", n, second)
	}
}

func TestEnsureGitignoreHonorsCustomRunsDir(t *testing.T) {
	repo := t.TempDir()
	if err := EnsureGitignore(repo, "build/trau-runs"); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	got := readGitignore(t, repo)
	if !strings.Contains(got, "build/trau-runs/") {
		t.Errorf("custom runs dir not ignored; got:\n%s", got)
	}
	if strings.Contains(got, ".trau/runs") {
		t.Errorf("should not ignore the default runs dir when a custom one is set; got:\n%s", got)
	}
}

func TestEnsureGitignorePreservesExistingContentAndCoverage(t *testing.T) {
	repo := t.TempDir()
	// Existing content with no trailing newline, and a broad ".trau/" rule that
	// already covers the runs dir — we should only add the missing .trau.ini and
	// must not duplicate the runs coverage.
	existing := "node_modules\n.trau/"
	gi := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(gi, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed .gitignore: %v", err)
	}
	if err := EnsureGitignore(repo, ".trau/runs"); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	got := readGitignore(t, repo)
	if !strings.Contains(got, "node_modules") {
		t.Errorf("clobbered existing content; got:\n%s", got)
	}
	if strings.Contains(got, ".trau/runs/") {
		t.Errorf("added redundant runs rule already covered by .trau/; got:\n%s", got)
	}
	if !strings.Contains(got, ".trau.ini") {
		t.Errorf("missing .trau.ini rule; got:\n%s", got)
	}
}

func TestEnsureGitignoreSkipsAbsoluteRunsDir(t *testing.T) {
	repo := t.TempDir()
	if err := EnsureGitignore(repo, "/var/tmp/trau-runs"); err != nil {
		t.Fatalf("EnsureGitignore: %v", err)
	}
	got := readGitignore(t, repo)
	if strings.Contains(got, "/var/tmp/trau-runs") {
		t.Errorf("absolute runs dir is not a repo-local pattern; got:\n%s", got)
	}
	// .trau.ini is still worth ignoring even when the runs dir lives elsewhere.
	if !strings.Contains(got, ".trau.ini") {
		t.Errorf("missing .trau.ini rule; got:\n%s", got)
	}
}

func TestEnsureGitignoreNoRepoRootIsNoop(t *testing.T) {
	if err := EnsureGitignore("", ".trau/runs"); err != nil {
		t.Errorf("empty repoRoot should be a no-op, got: %v", err)
	}
}
