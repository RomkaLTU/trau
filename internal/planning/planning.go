// Package planning is the durable core of trau's planning module: it turns a raw
// idea into a PRD by running planning rounds through the provider-agnostic agent
// Runner seam, one fresh isolated process per round (never continue/resume, like
// every other phase).
//
// A plan session lives under the runs area as its own directory — an idea
// snapshot, a checkpoint state file advancing through an ordered set of phases,
// and the persisted PRD once one is drafted — so a session survives a reboot and
// can be resumed. The agent returns a structured [Payload] through the result-file
// channel; this package parses and validates it and persists the outcome.
package planning

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/sanitize"
)

// Plan-session lifecycle phases, written to the session's state file under the
// PHASE key. A session advances through them in order; PhaseAborted is terminal.
// A drafted PRD rests at PhaseReview until the user approves it into PhasePRDReady;
// the later phases are defined here so the checkpoint format is stable for the
// rounds that publish and slice.
const (
	PhaseDrafting  = "drafting"
	PhaseQuestions = "questions"
	PhaseReview    = "prd_review"
	PhasePRDReady  = "prd_ready"
	PhasePublished = "published"
	PhaseSliced    = "sliced"
	PhaseAborted   = "aborted"
)

// phaseOrder is the forward progression of a plan session. PhaseAborted is not
// listed — it is a terminal side-exit reachable from any phase.
var phaseOrder = []string{PhaseDrafting, PhaseQuestions, PhaseReview, PhasePRDReady, PhasePublished, PhaseSliced}

// PhaseRank is the ordered rank of a plan-session phase: drafting(1) → questions(2)
// → prd_review(3) → prd_ready(4) → published(5) → sliced(6), aborted(9), and 0 for
// an unknown or empty phase. It mirrors state.Idx so resume logic can compare progress.
func PhaseRank(phase string) int {
	if phase == PhaseAborted {
		return 9
	}
	for i, p := range phaseOrder {
		if p == phase {
			return i + 1
		}
	}
	return 0
}

// Terminal reports whether a plan session is finished: fully sliced or aborted.
func Terminal(phase string) bool {
	return phase == PhaseSliced || phase == PhaseAborted
}

// Session is one durable plan session rooted at its own directory. The zero value
// is unusable — sessions are created by an [Orchestrator] or opened with
// [OpenSession].
type Session struct {
	dir string
	now func() time.Time
}

// Session file names within the session directory.
const (
	ideaFile  = "idea.md"
	stateFile = "state"
	prdFile   = "prd.md"
)

// OpenSession attaches to an existing session directory for reading its
// checkpoint and artifacts.
func OpenSession(dir string) *Session {
	return &Session{dir: dir, now: time.Now}
}

// Dir returns the session's directory.
func (s *Session) Dir() string { return s.dir }

// Idea returns the persisted idea snapshot.
func (s *Session) Idea() string {
	b, _ := os.ReadFile(filepath.Join(s.dir, ideaFile))
	return string(b)
}

// Phase returns the current checkpoint phase, or "" when none is recorded.
func (s *Session) Phase() string { return s.get("PHASE") }

// Epic returns the identifier of the tracker epic the PRD was published as, or ""
// when the session has not been published.
func (s *Session) Epic() string { return s.get("EPIC") }

// PRD returns the persisted PRD and whether one has been written.
func (s *Session) PRD() (PRD, bool) {
	b, err := os.ReadFile(filepath.Join(s.dir, prdFile))
	if err != nil {
		return PRD{}, false
	}
	return PRD{Title: s.get("PRD_TITLE"), Markdown: string(b)}, true
}

func (s *Session) writeIdea(idea string) error {
	return os.WriteFile(filepath.Join(s.dir, ideaFile), []byte(idea), 0o644)
}

// setPhase advances the checkpoint to phase.
func (s *Session) setPhase(phase string) error { return s.set("PHASE", phase) }

// savePRD persists the PRD markdown and its title, then advances the checkpoint to
// prd_review — a drafted PRD awaiting the user's approval. Every revision rewrites
// this same durable copy.
func (s *Session) savePRD(prd PRD) error {
	if err := os.WriteFile(filepath.Join(s.dir, prdFile), []byte(prd.Markdown), 0o644); err != nil {
		return err
	}
	if err := s.set("PRD_TITLE", prd.Title); err != nil {
		return err
	}
	return s.setPhase(PhaseReview)
}

// Approve records the reviewed PRD as final, advancing the checkpoint from
// prd_review to prd_ready. It is the closing step of the review loop; publishing the
// PRD to the tracker follows.
func (s *Session) Approve() error {
	if _, ok := s.PRD(); !ok {
		return fmt.Errorf("planning: no PRD to approve")
	}
	return s.setPhase(PhasePRDReady)
}

// markPublished records the created epic identifier and advances the checkpoint to
// published. The durable PRD copy is left in place — publishing adds a tracker copy,
// it does not replace the local one.
func (s *Session) markPublished(epic string) error {
	if err := s.set("EPIC", epic); err != nil {
		return err
	}
	return s.setPhase(PhasePublished)
}

// Abort marks the session aborted — a terminal side-exit reachable from any phase.
// It only flips the checkpoint; it never touches the tracker, so any issues an
// already-published session created are left untouched, and the publish and slice
// steps refuse a Terminal session so nothing further is ever created from it.
func (s *Session) Abort() error { return s.setPhase(PhaseAborted) }

// get returns the value of a state key, or "" when the file or key is absent.
func (s *Session) get(key string) string {
	data, err := os.ReadFile(filepath.Join(s.dir, stateFile))
	if err != nil {
		return ""
	}
	prefix := key + "="
	val := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			val = line[len(prefix):]
		}
	}
	return val
}

// set upserts key=value and refreshes UPDATED, last-write-wins per key, writing
// atomically via a temp file and rename. Values are sanitized to a single line so
// the KEY=value format stays readable and stable.
func (s *Session) set(key, value string) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	var kept []string
	if data, err := os.ReadFile(filepath.Join(s.dir, stateFile)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, key+"=") || strings.HasPrefix(line, "UPDATED=") {
				continue
			}
			kept = append(kept, line)
		}
	}
	kept = append(kept, key+"="+sanitize.StateValue(value))
	kept = append(kept, "UPDATED="+s.now().Format("2006-01-02 15:04:05"))
	out := strings.Join(kept, "\n") + "\n"

	tmp, err := os.CreateTemp(s.dir, "state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.dir, stateFile))
}

// newSession creates a fresh session directory rooted under root, snapshots the
// idea, and marks it drafting. The id is a sortable timestamp so sessions list in
// creation order under the runs area.
func newSession(root, idea string, now func() time.Time) (*Session, error) {
	id := now().Format("20060102-150405.000")
	id = strings.ReplaceAll(id, ".", "-")
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create plan session dir: %w", err)
	}
	s := &Session{dir: dir, now: now}
	if err := s.writeIdea(idea); err != nil {
		return nil, fmt.Errorf("write idea snapshot: %w", err)
	}
	if err := s.setPhase(PhaseDrafting); err != nil {
		return nil, fmt.Errorf("checkpoint drafting: %w", err)
	}
	return s, nil
}
