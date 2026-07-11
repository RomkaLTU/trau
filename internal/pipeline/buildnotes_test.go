package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const notesMarker = "The build agent left notes on this slice at"

func TestBuildNotesPersistRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := &Pipeline{RunsDir: dir}
	id := "COD-802-roundtrip"
	tmp := buildNotesPath(id)
	t.Cleanup(func() { _ = os.Remove(tmp) })

	body := "files: internal/pipeline/buildnotes.go\ntest: go test ./internal/pipeline/\n"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p.persistBuildNotes(id)
	durable := filepath.Join(dir, id, "buildnotes.md")
	if got, err := os.ReadFile(durable); err != nil {
		t.Fatalf("notes not persisted at %s: %v", durable, err)
	} else if string(got) != body {
		t.Fatalf("persisted notes = %q, want %q", got, body)
	}

	_ = os.Remove(tmp)
	p.restoreBuildNotes(id)
	if restored, err := os.ReadFile(tmp); err != nil {
		t.Fatalf("notes not restored to /tmp after reboot: %v", err)
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

	if got := handoffTail(id, ""); strings.Contains(got, buildNotesPath(id)) {
		t.Errorf("handoff prompt leaked the build notes:\n%s", got)
	}
	if got := verifyTail(id, handoffPath(id), verifyPath(id), "", "", "", "", ""); strings.Contains(got, buildNotesPath(id)) {
		t.Errorf("verify prompt leaked the build notes:\n%s", got)
	}
}

func TestBuildInstructionRequestsRedactedBestEffortNotes(t *testing.T) {
	got := buildInstruction("COD-802", "feature/x", "", "", "")
	mustContain(t, "buildInstruction", got,
		buildNotesPath("COD-802"),
		"redact any secrets",
		"optional",
	)
}

type namedPrompt struct{ name, got string }

func mechanicalPrompts(id, note string) []namedPrompt {
	return []namedPrompt{
		{"cleanup", cleanupInstruction(id, note)},
		{"repair", repairInstruction(id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", note, "")},
		{"bugfix", bugfixInstruction(id, verifyPath(id), handoffPath(id), "feature/x", "boom", "", "", note, "")},
		{"push-repair", pushRepairInstruction(id, "hook said no", note)},
	}
}
