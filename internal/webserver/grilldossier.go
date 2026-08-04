package webserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RomkaLTU/trau/internal/attachfile"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/state"
)

// grillDossierIndex is the dossier's entry point: it names the failure, carries the
// run's artifacts and lessons, and lists the absolute path of every phase log
// written beside it.
const grillDossierIndex = "dossier.md"

// grillDossierLogCap bounds one written phase log. A longer one keeps its tail,
// where the failure that ended the run is.
const grillDossierLogCap = 64 << 10

// grillDossierArtifacts is the artifact order the index renders, most diagnostic
// first — the verdict says what failed before the brief says what was asked for.
var grillDossierArtifacts = []struct{ kind, title string }{
	{hubstore.ArtifactVerdict, "Verdict"},
	{hubstore.ArtifactHandoff, "Hand-off brief"},
	{hubstore.ArtifactRubric, "Acceptance rubric"},
	{hubstore.ArtifactBuildNotes, "Build notes"},
}

// grillFailedRun is a quarantined or faulted run as a fix session reads it: the
// checkpoint fields the board derives, plus the branch that still holds the failed
// attempt. Only gave_up and faulted runs qualify — a paused or stopped run has
// nothing to diagnose.
type grillFailedRun struct {
	Ticket        string
	Phase         string
	Branch        string
	PR            string
	PRURL         string
	FailureClass  string
	FailureReason string
	Anomalies     string
	UpdatedAt     string
}

func grillFixable(class string) bool {
	return class == state.FailGaveUp || class == state.FailFaulted
}

// grillFailedRunFor reads the ticket's checkpoint and reports the failed run it
// records, or false when the run is gone or no longer failed — a requeue that raced
// the start clears the very fields the dossier is compiled from.
func (s *Server) grillFailedRunFor(root, ticket string) (grillFailedRun, bool) {
	row, found, err := s.stores.Checkpoints().One(root, ticket)
	if err != nil || !found {
		return grillFailedRun{}, false
	}
	run := runViewFromCheckpoint(hubstore.TicketCheckpoint{Ticket: ticket, CheckpointRow: row})
	if !grillFixable(run.FailureClass) {
		return grillFailedRun{}, false
	}
	return grillFailedRun{
		Ticket:        run.Ticket,
		Phase:         run.Phase,
		Branch:        run.Branch,
		PR:            run.PR,
		PRURL:         run.PRURL,
		FailureClass:  run.FailureClass,
		FailureReason: run.FailureReason,
		Anomalies:     checkpointField(row.Data, "ANOMALIES"),
		UpdatedAt:     run.UpdatedAt,
	}, true
}

// grillDossierPath is where a fix session's index lands: the same per-ticket
// materialization directory the session's attachments use, so the child reaches
// every seeded file by absolute path.
func grillDossierPath(ticket string) string {
	return filepath.Join(attachfile.Dir(ticket), grillDossierIndex)
}

// writeGrillDossier compiles the failure dossier for run and writes it into the
// ticket's materialization directory: an index naming the failure, the run's
// artifacts and the lessons it taught, plus one file per phase log. Pty transcripts
// are deliberately absent — they are the live view's replay, not the run's record.
// Missing logs, artifacts or lessons are stated in the index rather than failing the
// compile: an early fault produces none of them.
func (s *Server) writeGrillDossier(root string, run grillFailedRun) error {
	dir := attachfile.Dir(run.Ticket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dossier dir %s: %w", dir, err)
	}
	logs, err := s.stores.PhaseLogs().List(root, run.Ticket)
	if err != nil {
		return fmt.Errorf("read phase logs %s: %w", run.Ticket, err)
	}
	written, err := writeGrillPhaseLogs(dir, logs)
	if err != nil {
		return err
	}
	arts, err := s.stores.Artifacts().All(root, run.Ticket)
	if err != nil {
		return fmt.Errorf("read artifacts %s: %w", run.Ticket, err)
	}
	lessons, err := s.stores.Lessons().All(root)
	if err != nil {
		return fmt.Errorf("read lessons %s: %w", root, err)
	}
	index := grillDossierIndexBody(run, written, arts, ticketLessons(lessons, run.Ticket))
	path := filepath.Join(dir, grillDossierIndex)
	if err := os.WriteFile(path, []byte(index), 0o644); err != nil {
		return fmt.Errorf("write dossier %s: %w", path, err)
	}
	return nil
}

// grillPhaseLogFile is one phase log as written beside the index.
type grillPhaseLogFile struct {
	phase  string
	path   string
	tailed bool
}

func writeGrillPhaseLogs(dir string, logs []hubstore.PhaseLog) ([]grillPhaseLogFile, error) {
	out := make([]grillPhaseLogFile, 0, len(logs))
	for _, log := range logs {
		body, tailed := tailGrillLog(log.Content, grillDossierLogCap)
		if tailed {
			body = fmt.Sprintf("[tailed: showing the last %dKB of a %dKB log]\n\n%s", len(body)>>10, len(log.Content)>>10, body)
		}
		path := filepath.Join(dir, "phase-"+safeGrillPhase(log.Phase)+".log")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return nil, fmt.Errorf("write phase log %s: %w", path, err)
		}
		out = append(out, grillPhaseLogFile{phase: log.Phase, path: path, tailed: tailed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].phase < out[j].phase })
	return out, nil
}

// tailGrillLog keeps the last limit bytes of content, cut at the next line boundary
// so the excerpt opens on a whole line, and reports whether anything was dropped.
func tailGrillLog(content string, limit int) (string, bool) {
	if len(content) <= limit {
		return content, false
	}
	tail := content[len(content)-limit:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return tail, true
}

// safeGrillPhase reduces a phase name to a filename fragment; phases are already
// slugs, so this only guards a legacy row carrying a path separator.
func safeGrillPhase(phase string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, phase)
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}

// ticketLessons narrows the repo's ledger to what the failed ticket itself taught;
// the wider ledger is the next attempt's own recall, not this diagnosis's evidence.
func ticketLessons(all []hubstore.Lesson, ticket string) []hubstore.Lesson {
	out := make([]hubstore.Lesson, 0, len(all))
	for _, l := range all {
		if l.Ticket == ticket {
			out = append(out, l)
		}
	}
	return out
}

func grillDossierIndexBody(
	run grillFailedRun,
	logs []grillPhaseLogFile,
	arts map[string]string,
	lessons []hubstore.Lesson,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Failure dossier: %s\n\n## The run\n\n", run.Ticket)
	for _, f := range []struct{ label, value string }{
		{"Failed at phase", run.Phase},
		{"Failure class", run.FailureClass},
		{"Failure reason", run.FailureReason},
		{"WIP branch", run.Branch},
		{"Pull request", grillDossierPR(run)},
		{"Cost anomalies", run.Anomalies},
		{"Last checkpoint", run.UpdatedAt},
	} {
		if f.value != "" {
			fmt.Fprintf(&b, "- %s: %s\n", f.label, f.value)
		}
	}
	b.WriteString("\n## Phase logs\n\n")
	if len(logs) == 0 {
		b.WriteString("No phase logs were recorded — the run faulted before any phase produced output.\n")
	}
	for _, log := range logs {
		fmt.Fprintf(&b, "- %s — %s", log.phase, log.path)
		if log.tailed {
			b.WriteString(" (tailed to its last 64KB)")
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## Artifacts\n")
	missing := []string{}
	for _, a := range grillDossierArtifacts {
		body := strings.TrimSpace(arts[a.kind])
		if body == "" {
			missing = append(missing, a.title)
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n```\n%s\n```\n", a.title, body)
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\nNot recorded for this run: %s.\n", strings.Join(missing, ", "))
	}
	b.WriteString("\n## Lessons recorded for this ticket\n\n")
	if len(lessons) == 0 {
		b.WriteString("None — no repair pass distilled a lesson from this run.\n")
	}
	for _, l := range lessons {
		fmt.Fprintf(&b, "- %s", l.Lesson)
		if tag := strings.Trim(l.Phase+"/"+l.FailureType, "/"); tag != "" {
			fmt.Fprintf(&b, " (%s)", tag)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func grillDossierPR(run grillFailedRun) string {
	switch {
	case run.PR != "" && run.PRURL != "":
		return run.PR + " — " + run.PRURL
	case run.PRURL != "":
		return run.PRURL
	default:
		return run.PR
	}
}
