package pipeline

import (
	"os"
	"path/filepath"
	"strings"
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
func buildNotesInstruction(id string) string {
	return " As a best-effort aid to the later pipeline phases, after implementing jot a short build-notes file to exactly " + buildNotesPath(id) +
		" (overwrite if present) and nowhere else: the files you touched, the exact test command you ran for this slice, and any non-obvious decisions a later phase would otherwise have to rediscover. Keep it to a few lines and redact any secrets. This is optional — skipping it breaks nothing."
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

func (p *Pipeline) buildNotesRunsPath(id string) string {
	return filepath.Join(p.RunsDir, id, "buildnotes.md")
}

// persistBuildNotes mirrors the /tmp notes into runs/<ID>/buildnotes.md so they
// survive a reboot and a later resume. Best-effort and silent: absent or empty
// notes simply aren't mirrored.
func (p *Pipeline) persistBuildNotes(id string) {
	data, err := os.ReadFile(buildNotesPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	dir := filepath.Join(p.RunsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p.buildNotesRunsPath(id), data, 0o644)
}

// restoreBuildNotes copies the durable runs/<ID>/buildnotes.md back to /tmp when
// /tmp lost it (wiped on reboot), so a resumed cleanup/repair reuses the exact
// notes the build produced. Best-effort: it leaves /tmp untouched when a non-empty
// copy is already there or no durable copy exists.
func (p *Pipeline) restoreBuildNotes(id string) {
	if fi, err := os.Stat(buildNotesPath(id)); err == nil && fi.Size() > 0 {
		return
	}
	data, err := os.ReadFile(p.buildNotesRunsPath(id))
	if err != nil || len(data) == 0 {
		return
	}
	_ = os.WriteFile(buildNotesPath(id), data, 0o644)
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
