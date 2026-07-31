package webserver

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// seedSyncedFamily stores the tracker epic a re-grill is anchored to, plus the
// children a detach has to carry over with it.
func seedSyncedFamily(t *testing.T, stores *hubstore.Stores, root string, children ...string) {
	t.Helper()
	issues := []hubstore.Issue{{
		Identifier:  "COD-42",
		Title:       "Checkout rewrite",
		Description: "The old body.",
		StatusGroup: "unstarted",
		Labels:      []string{"needs-triage"},
	}}
	for i, title := range children {
		issues = append(issues, hubstore.Issue{
			Identifier:  fmt.Sprintf("COD-%d", 43+i),
			Title:       title,
			StatusGroup: "unstarted",
			Parent:      "COD-42",
		})
	}
	if _, _, err := stores.Issues().Upsert(root, "linear", issues); err != nil {
		t.Fatalf("seed synced family: %v", err)
	}
}

// detachStep returns the apply's detach step, which must always come first —
// nothing else may run against an identifier the internal store does not own yet.
func detachStep(t *testing.T, steps []GrillApplyStep) GrillApplyStep {
	t.Helper()
	if len(steps) == 0 || !strings.HasPrefix(steps[0].Step, "detach: ") {
		t.Fatalf("steps = %+v, want a leading detach step", steps)
	}
	return steps[0]
}

func hasDetachStep(steps []GrillApplyStep) bool {
	for _, step := range steps {
		if strings.HasPrefix(step.Step, "detach: ") {
			return true
		}
	}
	return false
}

func TestGrillApplyRewriteDetachesToInternal(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	seedSyncedFamily(t, stores, root, "Slice one")
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "A crisp new description.",
		Summary:             "clarified the flow",
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if res.StatusCode != http.StatusOK || !out.Applied {
		t.Fatalf("apply = %+v (status %d), want applied", out, res.StatusCode)
	}
	if step := detachStep(t, out.Steps); step.Step != "detach: COD-42 → ACME-1" || step.Status != grillStepOK {
		t.Fatalf("detach step = %+v, want COD-42 → ACME-1 landed", step)
	}
	if out.Session.IssueID != "ACME-1" || out.Session.IssueDestination != grillDestInternal {
		t.Errorf("session = %s/%s, want the internal anchor so a remount names it",
			out.Session.IssueID, out.Session.IssueDestination)
	}

	iss, found, err := stores.Issues().Internal(root, "ACME-1")
	if err != nil || !found {
		t.Fatalf("converted issue: found=%v err=%v", found, err)
	}
	if iss.Description != "A crisp new description." {
		t.Errorf("converted description = %q, want the rewrite applied to the internal root", iss.Description)
	}
	if hasLabel(iss.Labels, "needs-triage") || !hasLabel(iss.Labels, "ready-for-agent") {
		t.Errorf("converted labels = %v, want the triage label traded for the ready one", iss.Labels)
	}
	if _, found, _ := stores.Issues().Get(root, "COD-42"); found {
		t.Error("COD-42 is still on the board after the conversion")
	}
	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 1 {
		t.Fatalf("converted children = %+v (err=%v), want the sub-issue carried over", kids, err)
	}

	// The tracker keeps the retired tickets, told what happened and no longer ready.
	if len(fake.comments) != 2 {
		t.Fatalf("tracker comments = %+v, want the superseded note on both retired tickets", fake.comments)
	}
	if fake.comments[0].id != "COD-42" || !strings.Contains(fake.comments[0].body, "Superseded by ACME-1 in trau") {
		t.Errorf("superseded note = %+v, want it to name the internal issue", fake.comments[0])
	}
	for _, call := range fake.labels {
		if len(call.add) != 0 || !hasLabel(call.remove, "ready-for-agent") {
			t.Errorf("tracker label call = %+v, want only the ready label removed", call)
		}
	}
	if len(fake.descriptions) != 0 {
		t.Errorf("tracker descriptions = %+v, want the rewrite kept off the tracker", fake.descriptions)
	}
}

func TestGrillApplySplitDetachReusesConvertedChildren(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	seedSyncedFamily(t, stores, root, "Slice one")
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic framing.",
		Summary:             "sliced it",
		SubIssues: []grillSubIssue{
			{Title: "Slice one", Description: "already filed"},
			{Title: "Slice two", Description: "the new one"},
		},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied {
		t.Fatalf("apply = %+v, want applied", out)
	}
	detachStep(t, out.Steps)

	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	titles := make([]string, len(kids))
	for i, kid := range kids {
		titles[i] = kid.Title
	}
	if len(kids) != 2 {
		t.Fatalf("children = %v, want the converted slice reused and only the new one created", titles)
	}
	if len(fake.created) != 0 {
		t.Errorf("tracker creates = %+v, want the slices filed internally", fake.created)
	}
}

func TestGrillApplyDetachWarnsWhenTrackerUnreachable(t *testing.T) {
	tests := []struct {
		name    string
		writer  func(config.Config) (tracker.Writer, error)
		wantHas string
	}{
		{
			name:    "no credentials",
			writer:  func(config.Config) (tracker.Writer, error) { return nil, tracker.ErrWriterUnavailable },
			wantHas: "left as they were on the tracker",
		},
		{
			name: "tracker refuses the note",
			writer: func(config.Config) (tracker.Writer, error) {
				fake := newFakeWriter()
				fake.commentErr = errString("linear: 503")
				return fake, nil
			},
			wantHas: "COD-42: the superseded note failed: linear: 503",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, stores, root := grillApplyServerWriter(t, tt.writer)
			seedSyncedFamily(t, stores, root)
			sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
				Disposition:         grillDispRewrite,
				ProposedDescription: "A crisp new description.",
				Summary:             "clarified the flow",
			})

			_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
			if !out.Applied {
				t.Fatalf("apply = %+v, want the conversion to land whatever the tracker does", out)
			}
			if detachStep(t, out.Steps).Status != grillStepOK {
				t.Fatalf("detach step = %+v, want it landed", out.Steps[0])
			}
			if len(out.Warnings) == 0 || !strings.Contains(strings.Join(out.Warnings, "\n"), tt.wantHas) {
				t.Fatalf("warnings = %v, want one naming %q", out.Warnings, tt.wantHas)
			}
			if !slices.Equal(out.Session.ApplyWarnings, out.Warnings) {
				t.Errorf("settled session warnings = %v, want %v so a reopened card still raises them",
					out.Session.ApplyWarnings, out.Warnings)
			}
			if _, found, _ := stores.Issues().Internal(root, "ACME-1"); !found {
				t.Error("the conversion did not land")
			}
		})
	}
}

func TestGrillApplyDetachRefusedByQueue(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	seedSyncedFamily(t, stores, root, "Slice one")
	if _, err := stores.Queue(root).Add(queue.Item{ID: "COD-43", Kind: queue.KindTicket}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "A crisp new description.",
		Summary:             "clarified the flow",
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if out.Applied || len(out.Steps) != 1 {
		t.Fatalf("apply = %+v, want the refusal alone with nothing else attempted", out)
	}
	step := detachStep(t, out.Steps)
	if step.Status != grillStepFailed || !strings.Contains(step.Error, "COD-43") {
		t.Fatalf("detach step = %+v, want a failure naming the queued member", step)
	}
	if out.Session.State != hubstore.GrillFinished {
		t.Errorf("session state = %q, want finished so the user can retry", out.Session.State)
	}
	iss, found, err := stores.Issues().Get(root, "COD-42")
	if err != nil || !found || iss.Source == hubstore.SourceInternal {
		t.Errorf("COD-42 = %+v (found=%v err=%v), want it left on the tracker", iss, found, err)
	}
	if len(fake.comments) != 0 {
		t.Errorf("tracker comments = %+v, want none after a refusal", fake.comments)
	}
}

func TestGrillApplyDetachRetriesWithoutASecondConversion(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	seedSyncedFamily(t, stores, root)
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic framing.",
		Summary:             "sliced it",
	})

	// Pass one converts the ticket and then fails on a slice the store refuses,
	// leaving the session finished on the internal anchor.
	_, out := applyGrill(t, ts, sid, GrillApplyRequest{
		Destination: "internal",
		SubIssues:   []grillSubIssue{{Title: "", Description: "nameless"}},
	})
	if out.Applied || out.Session.IssueID != "ACME-1" {
		t.Fatalf("partial apply = %+v, want it converted but not applied", out)
	}
	if detachStep(t, out.Steps).Status != grillStepOK {
		t.Fatalf("detach step = %+v, want the conversion landed", out.Steps[0])
	}

	_, out = applyGrill(t, ts, sid, GrillApplyRequest{
		Destination: "internal",
		SubIssues:   []grillSubIssue{{Title: "Slice one", Description: "named this time"}},
	})
	if !out.Applied {
		t.Fatalf("retry = %+v, want applied", out)
	}
	if hasDetachStep(out.Steps) {
		t.Errorf("retry steps = %+v, want no second conversion — the anchor is internal now", out.Steps)
	}
	if out.Session.IssueID != "ACME-1" {
		t.Errorf("retry anchor = %q, want the same internal root", out.Session.IssueID)
	}
	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 1 {
		t.Fatalf("children = %+v (err=%v), want the one named slice", kids, err)
	}
	if len(fake.comments) != 1 {
		t.Errorf("tracker comments = %+v, want the superseded note posted once", fake.comments)
	}
}

func TestGrillApplyInternalAnchorSkipsDetach(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	iss, err := stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{
		Title:       "Already internal",
		Description: "The old body.",
	})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	sid := seedFinishedGrill(t, stores, root, iss.Identifier, grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "A crisp new description.",
		Summary:             "clarified the flow",
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied || hasDetachStep(out.Steps) {
		t.Fatalf("apply = %+v, want it applied with nothing to convert", out)
	}
	if n := len(fake.comments); n != 0 {
		t.Errorf("tracker comments = %d, want none for an issue that was never on it", n)
	}
}
