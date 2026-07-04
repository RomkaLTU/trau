package planning

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

// noopTracker satisfies tracker.Tracker with do-nothing methods, so a fake can embed
// it and add only the capability a test cares about. On its own it lacks the
// hierarchical-create capability — the graceful-degradation stand-in.
type noopTracker struct{}

func (noopTracker) Pick(context.Context, tracker.Scope) (string, error)           { return "", nil }
func (noopTracker) SubIssues(context.Context, string) ([]tracker.SubIssue, error) { return nil, nil }
func (noopTracker) Title(context.Context, string) (string, error)                 { return "", nil }
func (noopTracker) SetStatus(context.Context, string, string, string) error       { return nil }
func (noopTracker) Reset(context.Context, string) error                           { return nil }
func (noopTracker) Quarantine(context.Context, string, string) error              { return nil }
func (noopTracker) FileBug(context.Context, string, string) (string, error)       { return "", nil }
func (noopTracker) EnsureLabels(context.Context) error                            { return nil }

// recordingTracker adds the hierarchical-create capability and records the epic spec
// it was asked to create. It mirrors Linear's binding behaviour: a spec that carries
// no project lands in the tracker's own bound project, so a test can assert placement.
type recordingTracker struct {
	noopTracker
	project  string
	epic     string
	gotSpec  tracker.IssueSpec
	placedIn string
	calls    int
}

func (t *recordingTracker) CreateIssue(_ context.Context, spec tracker.IssueSpec) (string, error) {
	t.calls++
	t.gotSpec = spec
	t.placedIn = spec.Project
	if t.placedIn == "" {
		t.placedIn = t.project
	}
	return t.epic, nil
}

// TestPublishCreatesEpic is the orchestrator-level tracer for publishing: an approved
// PRD becomes an epic carrying the PRD as its description, placed in the tracker's
// bound project and never carrying a ready label, with the checkpoint advanced to
// published and the epic recorded while the local PRD copy stays put.
func TestPublishCreatesEpic(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"prd","prd":{"title":"Export widgets","markdown":"# Export widgets\n\nThe full PRD body."}}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "let users export widgets")
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	sess := rr.Session
	if err := sess.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	rec := &recordingTracker{project: "trau", epic: "COD-720"}
	res, err := o.Publish(context.Background(), sess, rec)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if rec.calls != 1 {
		t.Fatalf("CreateIssue called %d times, want 1", rec.calls)
	}
	if !strings.Contains(rec.gotSpec.Description, "The full PRD body.") {
		t.Errorf("epic description = %q, want the PRD markdown", rec.gotSpec.Description)
	}
	if rec.gotSpec.Title != "Export widgets" {
		t.Errorf("epic title = %q, want the PRD title", rec.gotSpec.Title)
	}
	if len(rec.gotSpec.Labels) != 0 {
		t.Errorf("epic carried labels %v, want none — an epic never gets the ready label", rec.gotSpec.Labels)
	}
	if rec.placedIn != "trau" {
		t.Errorf("epic placed in %q, want the bound project trau", rec.placedIn)
	}

	if !res.Published || res.Epic != "COD-720" {
		t.Errorf("result = %+v, want a published COD-720", res)
	}
	if got := sess.Phase(); got != PhasePublished {
		t.Errorf("phase = %q, want published", got)
	}
	if got := sess.Epic(); got != "COD-720" {
		t.Errorf("recorded epic = %q, want COD-720", got)
	}
	if _, err := os.Stat(filepath.Join(sess.Dir(), prdFile)); err != nil {
		t.Errorf("local PRD copy gone after publish: %v", err)
	}
	if prd, ok := sess.PRD(); !ok || !strings.Contains(prd.Markdown, "The full PRD body.") {
		t.Errorf("PRD not intact after publish: %+v", prd)
	}
}

// TestPublishWithoutCapabilityStaysLocal covers graceful degradation: a tracker that
// cannot create hierarchically publishes nothing, reports the skip, and leaves the
// session at prd_ready with its PRD still local.
func TestPublishWithoutCapabilityStaysLocal(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"prd","prd":{"title":"Export","markdown":"# Export\n\nbody"}}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())

	rr, err := o.RunRound(context.Background(), "an idea")
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	sess := rr.Session
	if err := sess.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	res, err := o.Publish(context.Background(), sess, noopTracker{})
	if err != nil {
		t.Fatalf("Publish should degrade gracefully, got error: %v", err)
	}
	if res.Published || res.Epic != "" {
		t.Errorf("result = %+v, want an unpublished skip", res)
	}
	if got := sess.Phase(); got != PhasePRDReady {
		t.Errorf("phase = %q, want it to stay at prd_ready", got)
	}
	if sess.Epic() != "" {
		t.Errorf("epic recorded on a skipped publish: %q", sess.Epic())
	}
	if _, err := os.Stat(filepath.Join(sess.Dir(), prdFile)); err != nil {
		t.Errorf("local PRD copy gone after a skipped publish: %v", err)
	}
}
