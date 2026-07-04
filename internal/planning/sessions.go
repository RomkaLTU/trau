package planning

import (
	"path/filepath"
	"sort"
	"strings"
)

// SessionInfo summarizes a durable plan session for the Plan screen's listing:
// where it lives, its checkpoint phase, and the human labels rendered per row.
// It is a read-only projection built by [List] from the session's artifacts.
type SessionInfo struct {
	Dir     string
	ID      string
	Phase   string
	Title   string
	Idea    string
	Updated string
}

// Resumable reports whether the session can be re-entered — any non-terminal phase.
func (si SessionInfo) Resumable() bool { return !Terminal(si.Phase) }

// List returns every plan session under root, resumable ones first and, within
// each group, newest first — the order the Plan screen surfaces in-flight work to
// resume above finished sessions kept for inspection or cleanup. Session ids are
// sortable creation timestamps, so a descending id sort is newest-first. A missing
// or unreadable root lists nothing rather than erroring.
func List(root string) []SessionInfo {
	matches, _ := filepath.Glob(filepath.Join(root, "*", stateFile))
	out := make([]SessionInfo, 0, len(matches))
	for _, m := range matches {
		dir := filepath.Dir(m)
		s := OpenSession(dir)
		si := SessionInfo{
			Dir:     dir,
			ID:      filepath.Base(dir),
			Phase:   s.Phase(),
			Updated: s.get("UPDATED"),
			Idea:    ideaSummary(s.Idea()),
		}
		if prd, ok := s.PRD(); ok {
			si.Title = prd.Title
		}
		out = append(out, si)
	}
	sort.SliceStable(out, func(i, j int) bool { return sessionLess(out[i], out[j]) })
	return out
}

// sessionLess orders resumable sessions ahead of terminal ones, then newest id
// first within each group.
func sessionLess(a, b SessionInfo) bool {
	if at, bt := Terminal(a.Phase), Terminal(b.Phase); at != bt {
		return !at
	}
	return a.ID > b.ID
}

// ideaSummary is the idea's first non-blank line, the label a session carries
// before it has a PRD title.
func ideaSummary(idea string) string {
	for _, line := range strings.Split(idea, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// ResumeStep is the point a plan session re-enters at when resumed, derived
// purely from its checkpoint phase. It mirrors pipeline resume: the checkpoint
// alone dictates where work picks back up, with nothing already settled replayed.
type ResumeStep string

const (
	// StepRound re-runs the current planning round from the idea and settled
	// transcript — for a session interrupted while drafting or answering questions.
	StepRound ResumeStep = "round"
	// StepReview reopens the drafted PRD for approval or a change request.
	StepReview ResumeStep = "review"
	// StepPublish re-enters at publishing the approved PRD to the tracker.
	StepPublish ResumeStep = "publish"
	// StepSlice re-enters at reviewing the published PRD's tracer-bullet slices.
	StepSlice ResumeStep = "slice"
	// StepDone marks a terminal session — sliced or aborted — with nothing to resume.
	StepDone ResumeStep = "done"
)

// ResumeStepFor maps a checkpoint phase to the step a resume re-enters at. Every
// phase resolves to exactly one step; an unknown phase is treated as terminal so a
// corrupt checkpoint is never mistaken for resumable work.
func ResumeStepFor(phase string) ResumeStep {
	switch phase {
	case PhaseDrafting, PhaseQuestions:
		return StepRound
	case PhaseReview:
		return StepReview
	case PhasePRDReady:
		return StepPublish
	case PhasePublished:
		return StepSlice
	default:
		return StepDone
	}
}
