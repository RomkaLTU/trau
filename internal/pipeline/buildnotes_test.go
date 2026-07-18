package pipeline

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/prompts"
)

const notesMarker = "The build agent left notes on this slice at"

// memArtifacts is an in-memory ArtifactStore for the pipeline's persist/restore
// tests — it stands in for the hub-backed store so a round-trip needs no serve
// process.
type memArtifacts struct{ m map[string]string }

func newMemArtifacts() *memArtifacts { return &memArtifacts{m: map[string]string{}} }

func (a *memArtifacts) Put(id, kind, content string) error {
	a.m[id+"/"+kind] = content
	return nil
}

func (a *memArtifacts) Get(id, kind string) (string, bool, error) {
	c, ok := a.m[id+"/"+kind]
	return c, ok, nil
}

func (a *memArtifacts) Remove(id string) error {
	for k := range a.m {
		if strings.HasPrefix(k, id+"/") {
			delete(a.m, k)
		}
	}
	return nil
}

// memPhaseLogs is an in-memory PhaseLogStore standing in for the hub-backed store
// so a pipeline test can read back what a phase persisted without a serve process.
// Its mutex mirrors the real store's concurrency safety: the overlapped build tail
// (handoff ∥ lintfix→cleanup) persists phase logs from two goroutines at once.
type memPhaseLogs struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemPhaseLogs() *memPhaseLogs { return &memPhaseLogs{m: map[string]string{}} }

func (l *memPhaseLogs) Put(id, phase, content string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[id+"/"+phase] = content
	return nil
}

func (l *memPhaseLogs) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.m {
		if strings.HasPrefix(k, id+"/") {
			delete(l.m, k)
		}
	}
	return nil
}

func (l *memPhaseLogs) get(id, phase string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.m[id+"/"+phase]
	return c, ok
}

func TestBuildNotesPersistRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := newMemArtifacts()
	p := &Pipeline{RunsDir: dir, Artifacts: store}
	id := "COD-802-roundtrip"
	tmp := buildNotesPath(id)
	t.Cleanup(func() { _ = os.Remove(tmp) })

	body := "files: internal/pipeline/buildnotes.go\ntest: go test ./internal/pipeline/\n"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p.persistBuildNotes(id)
	if got, ok := store.m[id+"/"+artifactBuildNotes]; !ok || got != body {
		t.Fatalf("notes not persisted to the store: %q ok=%v, want %q", got, ok, body)
	}

	_ = os.Remove(tmp)
	p.restoreBuildNotes(id)
	if restored, err := os.ReadFile(tmp); err != nil {
		t.Fatalf("notes not restored to /tmp from the store: %v", err)
	} else if string(restored) != body {
		t.Fatalf("restored notes = %q, want %q", restored, body)
	}

	if path, ok := p.activeBuildNotes(id); !ok || path != tmp {
		t.Fatalf("activeBuildNotes = (%q, %v), want (%q, true)", path, ok, tmp)
	}
}

func TestBuildNotesAbsentChangesNothing(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{RunsDir: dir}
	id := "COD-802-absent"
	tmp := buildNotesPath(id)
	_ = os.Remove(tmp)
	t.Cleanup(func() { _ = os.Remove(tmp) })

	if path, ok := p.activeBuildNotes(id); ok || path != "" {
		t.Fatalf("activeBuildNotes with no file = (%q, %v), want (\"\", false)", path, ok)
	}
	if got := buildNotesNote(""); got != "" {
		t.Fatalf("buildNotesNote(\"\") = %q, want empty", got)
	}

	if err := os.WriteFile(tmp, []byte("   \n\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.activeBuildNotes(id); ok {
		t.Fatal("a blank notes file was read as present")
	}

	for _, tc := range mechanicalPrompts(id, buildNotesNote("")) {
		if strings.Contains(tc.got, notesMarker) {
			t.Errorf("%s prompt injected a notes reference with no notes on disk:\n%s", tc.name, tc.got)
		}
	}
}

func TestBuildNotesInjectedIntoMechanicalPhasesOnly(t *testing.T) {
	id := "COD-802-inject"
	note := buildNotesNote(buildNotesPath(id))
	if !strings.Contains(note, notesMarker) || !strings.Contains(note, buildNotesPath(id)) {
		t.Fatalf("buildNotesNote dropped its marker/path: %q", note)
	}

	for _, tc := range mechanicalPrompts(id, note) {
		if !strings.Contains(tc.got, buildNotesPath(id)) {
			t.Errorf("%s prompt did not carry the build notes:\n%s", tc.name, tc.got)
		}
	}

	if got := handoffTail(prompts.Renderer{}, id, ""); strings.Contains(got, buildNotesPath(id)) {
		t.Errorf("handoff prompt leaked the build notes:\n%s", got)
	}
	if got := verifyTail(prompts.Renderer{}, id, handoffPath(id), verifyPath(id), "", "", "", "", ""); strings.Contains(got, buildNotesPath(id)) {
		t.Errorf("verify prompt leaked the build notes:\n%s", got)
	}
}

func TestBuildInstructionRequestsRedactedBestEffortNotes(t *testing.T) {
	got := buildInstruction(prompts.Renderer{}, "COD-802", "feature/x", "", "", "")
	mustContain(t, "buildInstruction", got,
		buildNotesPath("COD-802"),
		"redact any secrets",
		"optional",
	)
}

type namedPrompt struct{ name, got string }

func mechanicalPrompts(id, note string) []namedPrompt {
	return []namedPrompt{
		{"cleanup", cleanupInstruction(prompts.Renderer{}, id, note)},
		{"repair", repairInstruction(prompts.Renderer{}, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", note, "")},
		{"bugfix", bugfixInstruction(prompts.Renderer{}, id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", note, "")},
		{"push-repair", pushRepairInstruction(prompts.Renderer{}, id, "hook said no", note)},
	}
}
