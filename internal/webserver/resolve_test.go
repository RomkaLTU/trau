package webserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

func writeRepoINI(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.ProjectConfigName), []byte(body), 0o644); err != nil {
		t.Fatalf("write config in %s: %v", dir, err)
	}
}

// TestResolveReaderInfersJiraFromProjectCreds is the melga case: project-layer Jira
// creds and no TRACKER_PROVIDER, with a user-layer Linear key that would otherwise
// win the default. Resolution must pick jira, and a jira binding that is missing its
// project key must record what to set rather than the bare reader error.
func TestResolveReaderInfersJiraFromProjectCreds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeRepoINI(t, home, "LINEAR_API_KEY=user-linear-key\n")

	root := t.TempDir()
	writeRepoINI(t, root, "JIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.io\nJIRA_API_TOKEN=tok\n")

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))

	res, err := s.resolveReader(registry.Repo{Name: "acme", Root: root})
	if err != nil {
		t.Fatalf("resolveReader: %v", err)
	}
	if res.provider != "jira" {
		t.Fatalf("provider = %q, want jira", res.provider)
	}
	if res.reader == nil {
		t.Fatal("reader is nil, want a jira reader built from the project creds")
	}
	if res.explicit {
		t.Fatal("explicit = true, want false for an inferred provider")
	}

	got := res.actionableErr(tracker.ErrReaderUnavailable).Error()
	for _, want := range []string{"inferred jira", "PROJECT"} {
		if !strings.Contains(got, want) {
			t.Fatalf("actionableErr = %q, want it to mention %q", got, want)
		}
	}
}

// TestResolveReaderHonorsExplicitProvider pins the unchanged path: an explicit
// TRACKER_PROVIDER wins even when full project Jira creds are present, and its
// failures surface unrewritten.
func TestResolveReaderHonorsExplicitProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeRepoINI(t, home, "LINEAR_API_KEY=user-linear-key\n")

	root := t.TempDir()
	writeRepoINI(t, root, "TRACKER_PROVIDER=linear\nLINEAR_TEAM=COD\nJIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.io\nJIRA_API_TOKEN=tok\n")

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))

	res, err := s.resolveReader(registry.Repo{Name: "acme", Root: root})
	if err != nil {
		t.Fatalf("resolveReader: %v", err)
	}
	if res.provider != "linear" {
		t.Fatalf("provider = %q, want linear (explicit)", res.provider)
	}
	if !res.explicit {
		t.Fatal("explicit = false, want true")
	}
	if got := res.actionableErr(tracker.ErrReaderUnavailable); !errors.Is(got, tracker.ErrReaderUnavailable) {
		t.Fatalf("actionableErr rewrote an explicit provider's error: %v", got)
	}
}

// TestResolveReaderReportsJiraCredsWhenLinearTried covers the tie-break miss: Jira
// creds are present but not fully project-layer, so inference does not fire and
// linear is tried — the recorded error must still name the Jira mismatch.
func TestResolveReaderReportsJiraCredsWhenLinearTried(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeRepoINI(t, home, "LINEAR_API_KEY=user-linear-key\nJIRA_API_TOKEN=tok\n")

	root := t.TempDir()
	writeRepoINI(t, root, "JIRA_BASE_URL=https://acme.atlassian.net\nJIRA_EMAIL=dev@acme.io\n")

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))

	res, err := s.resolveReader(registry.Repo{Name: "acme", Root: root})
	if err != nil {
		t.Fatalf("resolveReader: %v", err)
	}
	if res.provider != "linear" {
		t.Fatalf("provider = %q, want linear (jira creds not fully project-layer)", res.provider)
	}
	if !res.jiraCreds {
		t.Fatal("jiraCreds = false, want true")
	}

	got := res.actionableErr(errors.New("linear: issue not found")).Error()
	for _, want := range []string{"Jira credentials", "TRACKER_PROVIDER=jira", "tried linear"} {
		if !strings.Contains(got, want) {
			t.Fatalf("actionableErr = %q, want it to mention %q", got, want)
		}
	}
}

// TestDefaultReaderUnavailableForInternal keeps a repo with no external tracker on
// the graceful no-credentials path instead of the unknown-provider error.
func TestDefaultReaderUnavailableForInternal(t *testing.T) {
	if _, err := defaultReader(config.Config{TrackerProvider: "linear"}); !errors.Is(err, tracker.ErrReaderUnavailable) {
		t.Fatalf("defaultReader err = %v, want ErrReaderUnavailable", err)
	}
}
