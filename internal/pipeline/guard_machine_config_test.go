package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
)

// sideEffectRunner runs an arbitrary filesystem mutation in place of a real agent
// process, modelling what a full-shell phase agent can do to the target repo.
type sideEffectRunner struct{ do func() }

func (r sideEffectRunner) Run(context.Context, string, string) (agent.Result, error) {
	if r.do != nil {
		r.do()
	}
	return agent.Result{Final: "done"}, nil
}

func TestAgentPhaseRestoresDeletedMachineConfig(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, config.ProjectConfigName)
	want := []byte("LINEAR_API_KEY=secret\nPROVIDER=claude\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := sideEffectRunner{do: func() { _ = os.Remove(path) }}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.RepoRoot = repo

	if _, err := p.agentPhaseOn(context.Background(), "COD-1", "build", "prompt", runner); err != nil {
		t.Fatalf("agentPhaseOn: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not restored: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored content = %q, want %q", got, want)
	}
}

func TestAgentPhaseKeepsEditedMachineConfig(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, config.ProjectConfigName)
	if err := os.WriteFile(path, []byte("OLD=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := []byte("NEW=2\n")

	runner := sideEffectRunner{do: func() { _ = os.WriteFile(path, edited, 0o600) }}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.RepoRoot = repo

	if _, err := p.agentPhaseOn(context.Background(), "COD-2", "build", "prompt", runner); err != nil {
		t.Fatalf("agentPhaseOn: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatalf("a live agent edit was clobbered: got %q, want %q", got, edited)
	}
}
