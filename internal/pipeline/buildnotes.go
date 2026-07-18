package pipeline

import (
	"os"
	"strings"

	"github.com/RomkaLTU/trau/internal/prompts"
)

// buildNotesPath is the /tmp file the build agent jots its slice notes to — the
// files it touched, the test command it ran, and any non-obvious decisions. It is
// the third durable inter-phase artifact after the QA brief and the rubric, but it
// only ever flows forward to the mechanical phases (cleanup, repair, bugfix,
// push-repair); it is deliberately withheld from handoff and verify so those keep
// re-deriving everything cold from the ticket and the code on disk.
func buildNotesPath(id string) string { return "/tmp/buildnotes-" + id + ".md" }

// buildNotesInstruction is appended to the build prompt. It is best-effort by
// design: an agent that ignores it leaves no file, and every downstream phase then
// behaves exactly as it does today.
func buildNotesInstruction(r prompts.Renderer, id string) string {
	return r.Render("build_notes", prompts.BuildNotesData{ID: id, Path: buildNotesPath(id)})
}

// readBuildNotes reads the notes at path. ok is false when the file is absent or
// blank, so a missing or empty notes file reads as "no notes" and the consuming
// phase injects nothing.
func readBuildNotes(path string) (notes string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", false
	}
	return string(data), true
}

// persistBuildNotes stores the /tmp notes through the hub so they survive a
// reboot and a later resume. Best-effort and silent: absent or empty notes simply
// aren't stored.
func (p *Pipeline) persistBuildNotes(id string) {
	data, err := os.ReadFile(buildNotesPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	p.putArtifact(id, artifactBuildNotes, string(data))
}

// restoreBuildNotes copies the durable notes back to /tmp when /tmp lost it
// (wiped on reboot), so a resumed cleanup/repair reuses the exact notes the build
// produced. Best-effort: it leaves /tmp untouched when a non-empty copy is already
// there or the hub holds none.
func (p *Pipeline) restoreBuildNotes(id string) {
	if fi, err := os.Stat(buildNotesPath(id)); err == nil && fi.Size() > 0 {
		return
	}
	content, ok := p.getArtifact(id, artifactBuildNotes)
	if !ok || content == "" {
		return
	}
	_ = os.WriteFile(buildNotesPath(id), []byte(content), 0o644)
}

// activeBuildNotes returns the /tmp notes path when non-empty notes are on disk
// for id (restoring the durable copy first), or ("", false) when none are present.
// Phase prompts use the path to point the agent at the notes and omit the
// reference entirely when ok is false.
func (p *Pipeline) activeBuildNotes(id string) (path string, ok bool) {
	p.restoreBuildNotes(id)
	if _, present := readBuildNotes(buildNotesPath(id)); !present {
		return "", false
	}
	return buildNotesPath(id), true
}

// buildNotesNote tells a mechanical phase (cleanup, repair, bugfix, push-repair)
// where the build agent's notes are so it can skip most re-exploration. Empty when
// path is "" (no notes on disk), so an absent notes file never injects a dangling
// file reference and the phase runs exactly as it does today.
func buildNotesNote(path string) string {
	if path == "" {
		return ""
	}
	return " The build agent left notes on this slice at " + path +
		" (files it touched, the test command it ran, and non-obvious decisions) — use them to skip re-exploring the repo. They are an informational shortcut, not authoritative: trust the code on disk if the two disagree."
}
