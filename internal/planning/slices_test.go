package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// creatingTracker adds the hierarchical-create capability and records every spec
// in creation order, handing out sequential identifiers. failAt (1-based call
// number) scripts a mid-list failure.
type creatingTracker struct {
	noopTracker
	specs  []tracker.IssueSpec
	failAt int
}

func (t *creatingTracker) CreateIssue(_ context.Context, spec tracker.IssueSpec) (string, error) {
	if t.failAt > 0 && len(t.specs)+1 == t.failAt {
		return "", errors.New("boom")
	}
	t.specs = append(t.specs, spec)
	return fmt.Sprintf("COD-%d", 900+len(t.specs)), nil
}

// publishedSession builds a session resting at published with a PRD and a
// recorded epic — the state the slice round starts from.
func publishedSession(t *testing.T, root string) *Session {
	t.Helper()
	sess, err := newSession(root, "let users export widgets", fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.savePRD(PRD{Title: "Export widgets", Markdown: "# Export widgets\n\nThe full PRD body."}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Approve(); err != nil {
		t.Fatal(err)
	}
	if err := sess.markPublished("COD-800"); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestValidateSlices(t *testing.T) {
	cases := []struct {
		name    string
		slices  []Slice
		wantErr string
	}{
		{
			name:    "empty list",
			slices:  nil,
			wantErr: "has no slices",
		},
		{
			name:    "missing title",
			slices:  []Slice{{Title: "first"}, {Title: "  "}},
			wantErr: "slice 1 missing title",
		},
		{
			name:    "unknown after reference",
			slices:  []Slice{{Title: "first", After: []string{"nowhere"}}},
			wantErr: `unknown "after" reference "nowhere"`,
		},
		{
			name:    "after reference to a later slice",
			slices:  []Slice{{Title: "first", After: []string{"second"}}, {Title: "second"}},
			wantErr: `unknown "after" reference "second"`,
		},
		{
			name:    "self reference",
			slices:  []Slice{{Title: "first", After: []string{"first"}}},
			wantErr: `unknown "after" reference "first"`,
		},
		{
			name:   "valid chain",
			slices: []Slice{{Title: "first"}, {Title: "second", After: []string{"first"}}, {Title: "third", After: []string{"first", "second"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSlices(tc.slices)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateSlices: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateSlices error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestSliceRoundDrafts covers the slice round itself: a published session runs a
// fresh agent process under the slice label with the PRD and the tracer-bullet
// conventions in the prompt, and the drafts come back without moving the
// checkpoint or touching anything durable — they exist to be reviewed.
func TestSliceRoundDrafts(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"slices","slices":[{"title":"first"},{"title":"second","after":["first"]}]}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())
	sess := publishedSession(t, root)

	rr, err := o.SliceRound(context.Background(), sess)
	if err != nil {
		t.Fatalf("SliceRound: %v", err)
	}
	if runner.gotLabel != agent.PhaseSlice {
		t.Errorf("slice round ran under label %q, want %q", runner.gotLabel, agent.PhaseSlice)
	}
	if !strings.Contains(runner.gotPrompt, "The full PRD body.") {
		t.Error("prompt did not carry the published PRD")
	}
	for _, convention := range []string{"tracer-bullet", "vertical slice", "demoable", "thin slices"} {
		if !strings.Contains(runner.gotPrompt, convention) {
			t.Errorf("prompt missing the %q convention", convention)
		}
	}
	if rr.Payload.Status != StatusSlices || len(rr.Payload.Slices) != 2 {
		t.Fatalf("payload = %+v, want two slice drafts", rr.Payload)
	}
	if got := sess.Phase(); got != PhasePublished {
		t.Errorf("phase after drafting = %q, want to stay published", got)
	}
}

func TestSliceRoundGuards(t *testing.T) {
	root := t.TempDir()
	o := NewOrchestrator(&fakeRunner{final: `{"status":"slices","slices":[{"title":"first"}]}`}, root).WithClock(fixedClock())

	t.Run("unpublished session", func(t *testing.T) {
		sess, err := newSession(root, "an idea", fixedClock())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.SliceRound(context.Background(), sess); err == nil || !strings.Contains(err.Error(), "no published epic") {
			t.Errorf("err = %v, want a no-epic refusal", err)
		}
	})

	t.Run("aborted session", func(t *testing.T) {
		sess := publishedSession(t, t.TempDir())
		if err := sess.Abort(); err != nil {
			t.Fatal(err)
		}
		if _, err := o.SliceRound(context.Background(), sess); err == nil || !strings.Contains(err.Error(), "nothing to slice") {
			t.Errorf("err = %v, want a terminal refusal", err)
		}
	})

	t.Run("non-slices payload", func(t *testing.T) {
		sess := publishedSession(t, t.TempDir())
		op := NewOrchestrator(&fakeRunner{final: `{"status":"prd","prd":{"title":"T","markdown":"x"}}`}, root)
		if _, err := op.SliceRound(context.Background(), sess); err == nil || !strings.Contains(err.Error(), "want slices") {
			t.Errorf("err = %v, want a wrong-status rejection", err)
		}
	})
}

// TestCreateSlicesAfterReview is the orchestrator-level tracer for the whole slice
// step: a scripted slices payload is drafted, the review edits a title, drops a
// slice, and reorders the rest — exactly the TUI verbs — and the confirmed drafts
// land on the fake tracker as children of the epic, in the reviewed order, each
// carrying the ready label the epic itself never gets, with "after" dependencies
// rendered against the real created identifiers and the checkpoint at sliced.
func TestCreateSlicesAfterReview(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{final: `{"status":"slices","slices":[` +
		`{"title":"scaffold export","description":"## What to build\n\nthe seam","labels":["backend"]},` +
		`{"title":"csv download","description":"## What to build\n\ncsv","after":["scaffold export"]},` +
		`{"title":"telemetry","description":"## What to build\n\nmetrics","after":["scaffold export"]},` +
		`{"title":"pdf export","description":"## What to build\n\npdf","after":["scaffold export"]}]}`}
	o := NewOrchestrator(runner, root).WithClock(fixedClock())
	sess := publishedSession(t, root)

	rr, err := o.SliceRound(context.Background(), sess)
	if err != nil {
		t.Fatalf("SliceRound: %v", err)
	}
	drafts := rr.Payload.Slices

	// The review: retitle the first slice (references follow), drop the pdf
	// slice, and move telemetry ahead of the csv download.
	drafts[0].Title = "export seam"
	for i := range drafts {
		for j, ref := range drafts[i].After {
			if ref == "scaffold export" {
				drafts[i].After[j] = "export seam"
			}
		}
	}
	reviewed := []Slice{drafts[0], drafts[2], drafts[1]}

	rec := &creatingTracker{}
	res, err := o.CreateSlices(context.Background(), sess, rec, reviewed, "ready-for-agent")
	if err != nil {
		t.Fatalf("CreateSlices: %v", err)
	}

	if len(rec.specs) != 3 {
		t.Fatalf("created %d children, want 3 (the dropped slice must not exist)", len(rec.specs))
	}
	wantTitles := []string{"export seam", "telemetry", "csv download"}
	for i, spec := range rec.specs {
		if spec.Title != wantTitles[i] {
			t.Errorf("child %d title = %q, want %q (reviewed order)", i, spec.Title, wantTitles[i])
		}
		if spec.Parent != "COD-800" {
			t.Errorf("child %d parent = %q, want the epic COD-800", i, spec.Parent)
		}
		if spec.Project != "" {
			t.Errorf("child %d project = %q, want empty (the tracker's PROJECT binding)", i, spec.Project)
		}
		hasReady := false
		for _, l := range spec.Labels {
			hasReady = hasReady || l == "ready-for-agent"
		}
		if !hasReady {
			t.Errorf("child %d labels = %v, want the ready label", i, spec.Labels)
		}
	}
	if got := rec.specs[0].Labels; len(got) != 2 || got[0] != "backend" {
		t.Errorf("first child labels = %v, want the drafted label plus ready", got)
	}
	if !strings.Contains(rec.specs[1].Description, "## Blocked by") || !strings.Contains(rec.specs[1].Description, "* COD-901") {
		t.Errorf("telemetry description = %q, want a Blocked by section naming the created COD-901", rec.specs[1].Description)
	}
	if strings.Contains(rec.specs[0].Description, "## Blocked by") {
		t.Error("an unblocked slice grew a Blocked by section")
	}

	if !res.Created || len(res.Children) != 3 || res.Children[0] != "COD-901" {
		t.Errorf("result = %+v, want three created children starting at COD-901", res)
	}
	if got := sess.Phase(); got != PhaseSliced {
		t.Errorf("phase = %q, want sliced", got)
	}
}

// TestCreateSlicesWithoutCapabilityStaysPublished mirrors publish's graceful
// degradation: a tracker that cannot create children makes nothing and the
// session stays at published.
func TestCreateSlicesWithoutCapabilityStaysPublished(t *testing.T) {
	root := t.TempDir()
	o := NewOrchestrator(&fakeRunner{}, root).WithClock(fixedClock())
	sess := publishedSession(t, root)

	res, err := o.CreateSlices(context.Background(), sess, noopTracker{}, []Slice{{Title: "first"}}, "ready-for-agent")
	if err != nil {
		t.Fatalf("CreateSlices should degrade gracefully, got: %v", err)
	}
	if res.Created || len(res.Children) != 0 {
		t.Errorf("result = %+v, want an uncreated skip", res)
	}
	if got := sess.Phase(); got != PhasePublished {
		t.Errorf("phase = %q, want it to stay published", got)
	}
}

// TestCreateSlicesRejectsInvalidDrafts checks the reviewed drafts are re-validated
// before anything is created — a reorder that breaks a dependency creates nothing.
func TestCreateSlicesRejectsInvalidDrafts(t *testing.T) {
	root := t.TempDir()
	o := NewOrchestrator(&fakeRunner{}, root).WithClock(fixedClock())
	sess := publishedSession(t, root)
	rec := &creatingTracker{}

	reviewed := []Slice{{Title: "second", After: []string{"first"}}, {Title: "first"}}
	if _, err := o.CreateSlices(context.Background(), sess, rec, reviewed, "ready-for-agent"); err == nil {
		t.Fatal("CreateSlices should reject a dependency on a later slice")
	}
	if len(rec.specs) != 0 {
		t.Errorf("created %d children from invalid drafts, want none", len(rec.specs))
	}
	if got := sess.Phase(); got != PhasePublished {
		t.Errorf("phase = %q, want it to stay published", got)
	}
}

// TestCreateSlicesMidFailureStaysPublished checks a failure partway leaves the
// checkpoint at published and reports what was created, so the session stays
// resumable rather than pretending it finished.
func TestCreateSlicesMidFailureStaysPublished(t *testing.T) {
	root := t.TempDir()
	o := NewOrchestrator(&fakeRunner{}, root).WithClock(fixedClock())
	sess := publishedSession(t, root)
	rec := &creatingTracker{failAt: 2}

	res, err := o.CreateSlices(context.Background(), sess, rec, []Slice{{Title: "first"}, {Title: "second"}}, "ready-for-agent")
	if err == nil {
		t.Fatal("CreateSlices should surface the mid-list failure")
	}
	if len(res.Children) != 1 || res.Children[0] != "COD-901" {
		t.Errorf("children = %v, want the one created before the failure", res.Children)
	}
	if got := sess.Phase(); got != PhasePublished {
		t.Errorf("phase = %q, want it to stay published after a partial failure", got)
	}
}
