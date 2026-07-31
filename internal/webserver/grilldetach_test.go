package webserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestGrillApplyDetachWarningsSurviveTheRetry(t *testing.T) {
	ts, stores, root := grillApplyServerWriter(t, func(config.Config) (tracker.Writer, error) {
		return nil, tracker.ErrWriterUnavailable
	})
	seedSyncedFamily(t, stores, root)
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic framing.",
		Summary:             "sliced it",
	})

	// Pass one converts the ticket with the tracker out of reach, then fails on a
	// slice the store refuses.
	_, out := applyGrill(t, ts, sid, GrillApplyRequest{
		Destination: "internal",
		SubIssues:   []grillSubIssue{{Title: "", Description: "nameless"}},
	})
	if out.Applied || len(out.Warnings) == 0 {
		t.Fatalf("partial apply = %+v, want it unapplied and warning about the tickets left on the tracker", out)
	}
	if !slices.Equal(out.Session.ApplyWarnings, out.Warnings) {
		t.Fatalf("partial session warnings = %v, want %v recorded before the retry", out.Session.ApplyWarnings, out.Warnings)
	}

	_, retry := applyGrill(t, ts, sid, GrillApplyRequest{
		Destination: "internal",
		SubIssues:   []grillSubIssue{{Title: "Slice one", Description: "named this time"}},
	})
	if !retry.Applied {
		t.Fatalf("retry = %+v, want applied", retry)
	}
	if !slices.Equal(retry.Session.ApplyWarnings, out.Warnings) {
		t.Errorf("settled session warnings = %v, want %v — the retry converts nothing, so nothing else can raise them",
			retry.Session.ApplyWarnings, out.Warnings)
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

// fileCreateEpicOnTracker files a create outcome on the tracker and leaves the
// session finished: the tracker refuses the summary comment, so the pass never
// settles and the parent it filed stays the session's anchor. The writer is reset
// afterwards, so a test sees only what the re-apply does.
func fileCreateEpicOnTracker(
	t *testing.T,
	ts *httptest.Server,
	stores *hubstore.Stores,
	root string,
	fake *fakeWriter,
	creates []fakeCreate,
) int64 {
	t.Helper()
	fake.createQueue = creates
	fake.commentErr = errString("linear: 502")
	sid := seedFinishedGrill(t, stores, root, "", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "New epic",
		ProposedDescription: "Epic body.",
		Summary:             "one slice",
		SubIssues:           []grillSubIssue{{Title: "S1", Description: "d1"}},
	})
	if _, out := applyGrill(t, ts, sid, GrillApplyRequest{}); out.Applied || out.Session.IssueID != "COD-300" {
		t.Fatalf("tracker apply = %+v, want the epic filed on COD-300 with the session left finished", out)
	}
	fake.commentErr = nil
	fake.comments = nil
	fake.created = nil
	fake.createQueue = nil
	fake.createIdx = 0
	return sid
}

func TestGrillApplyCreateSwitchConvertsFiledEpic(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	sid := fileCreateEpicOnTracker(t, ts, stores, root, fake, []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-300"}},
		{issue: tracker.NewIssue{Identifier: "COD-301"}},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied {
		t.Fatalf("internal re-apply = %+v, want applied", out)
	}
	if step := detachStep(t, out.Steps); step.Step != "detach: COD-300 → ACME-1" || step.Status != grillStepOK {
		t.Fatalf("detach step = %+v, want COD-300 → ACME-1 landed", step)
	}
	if len(fake.created) != 0 {
		t.Fatalf("re-apply filed %+v on the tracker, want the converted epic reused instead", fake.created)
	}
	if out.Session.IssueID != "ACME-1" || out.Session.IssueDestination != grillDestInternal {
		t.Errorf("session = %s/%s, want the converted epic as the internal anchor",
			out.Session.IssueID, out.Session.IssueDestination)
	}
	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 1 || kids[0].Title != "S1" {
		t.Fatalf("converted children = %+v (err=%v), want the filed slice carried over, not filed again", kids, err)
	}
	for _, retired := range []string{"COD-300", "COD-301"} {
		if _, found, _ := stores.Issues().Get(root, retired); found {
			t.Errorf("%s is still on the board beside the internal issue it became", retired)
		}
	}
	if _, _, err := stores.Issues().Upsert(root, "linear", []hubstore.Issue{
		{Identifier: "COD-300", Title: "New epic", StatusGroup: "unstarted"},
	}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if _, found, _ := stores.Issues().Get(root, "COD-300"); found {
		t.Error("a later sync re-imported the retired epic")
	}

	if len(fake.comments) != 2 || fake.comments[0].id != "COD-300" {
		t.Fatalf("tracker comments = %+v, want the superseded note on both retired tickets", fake.comments)
	}
	if !strings.Contains(fake.comments[0].body, "Superseded by ACME-1 in trau") {
		t.Errorf("superseded note = %+v, want it to name the internal issue", fake.comments[0])
	}
	if len(fake.labels) != 2 {
		t.Fatalf("tracker label calls = %+v, want the ready label pulled off both retired tickets", fake.labels)
	}
	for _, call := range fake.labels {
		if len(call.add) != 0 || !hasLabel(call.remove, "ready-for-agent") {
			t.Errorf("tracker label call = %+v, want only the ready label removed", call)
		}
	}
}

func TestGrillApplyCreateOnGrilledTicketFilesBesideIt(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	seedSyncedFamily(t, stores, root, "Slice one")
	sid := seedFinishedGrill(t, stores, root, "COD-42", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "New epic",
		ProposedDescription: "Epic body.",
		Summary:             "one slice",
		SubIssues:           []grillSubIssue{{Title: "S1", Description: "d1"}},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied || hasDetachStep(out.Steps) {
		t.Fatalf("apply = %+v, want a fresh internal epic with the grilled ticket left alone", out)
	}
	if out.Session.IssueID != "ACME-1" {
		t.Errorf("anchor = %q, want the internal epic the create filed", out.Session.IssueID)
	}
	iss, found, err := stores.Issues().Internal(root, "ACME-1")
	if err != nil || !found || iss.Title != "New epic" {
		t.Fatalf("internal epic = %+v (found=%v err=%v), want the created title", iss, found, err)
	}
	if _, found, _ := stores.Issues().Get(root, "COD-42"); !found {
		t.Error("the ticket the session was grilled on was converted away")
	}
	if len(fake.comments) != 0 {
		t.Errorf("tracker comments = %+v, want none when nothing was converted", fake.comments)
	}
}

func TestGrillApplyCreateSwitchWarnsWhenTrackerUnreachable(t *testing.T) {
	fake := newFakeWriter()
	unreachable := false
	ts, stores, root := grillApplyServerWriter(t, func(config.Config) (tracker.Writer, error) {
		if unreachable {
			return nil, tracker.ErrWriterUnavailable
		}
		return fake, nil
	})
	sid := fileCreateEpicOnTracker(t, ts, stores, root, fake, []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-300"}},
		{issue: tracker.NewIssue{Identifier: "COD-301"}},
	})

	unreachable = true
	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied {
		t.Fatalf("internal re-apply = %+v, want the conversion to land whatever the tracker does", out)
	}
	if detachStep(t, out.Steps).Status != grillStepOK {
		t.Fatalf("detach step = %+v, want it landed", out.Steps[0])
	}
	if len(out.Warnings) == 0 || !strings.Contains(strings.Join(out.Warnings, "\n"), "left as they were on the tracker") {
		t.Fatalf("warnings = %v, want one naming the tickets the conversion could not reach", out.Warnings)
	}
	if !slices.Equal(out.Session.ApplyWarnings, out.Warnings) {
		t.Errorf("settled session warnings = %v, want %v so a reopened card still raises them",
			out.Session.ApplyWarnings, out.Warnings)
	}
	if _, found, err := stores.Issues().Internal(root, "ACME-1"); err != nil || !found {
		t.Errorf("converted epic: found=%v err=%v", found, err)
	}
	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 1 || kids[0].Title != "S1" {
		t.Fatalf("converted children = %+v (err=%v), want the filed slice carried over", kids, err)
	}
}

func TestGrillApplyCreateFileInternallyConvertsPartialParent(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	// What the recovery button recovers from: the epic landed, its slice did not.
	sid := fileCreateEpicOnTracker(t, ts, stores, root, fake, []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-300"}},
		{err: errString("linear: 502")},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{Destination: "internal"})
	if !out.Applied {
		t.Fatalf("internal re-apply = %+v, want applied", out)
	}
	if step := detachStep(t, out.Steps); step.Step != "detach: COD-300 → ACME-1" || step.Status != grillStepOK {
		t.Fatalf("detach step = %+v, want the partly-filed parent converted", step)
	}
	if len(fake.created) != 0 {
		t.Fatalf("re-apply filed %+v on the tracker, want the missing slice filed internally", fake.created)
	}
	if out.Session.IssueID != "ACME-1" {
		t.Errorf("re-anchor = %q, want the converted parent", out.Session.IssueID)
	}
	kids, err := stores.Issues().InternalChildren(root, "ACME-1")
	if err != nil || len(kids) != 1 || kids[0].Title != "S1" {
		t.Fatalf("children = %+v (err=%v), want the missing slice created under the converted parent", kids, err)
	}
	if _, found, _ := stores.Issues().Get(root, "COD-300"); found {
		t.Error("COD-300 is still on the board after the conversion")
	}
	if len(fake.comments) != 1 || fake.comments[0].id != "COD-300" {
		t.Fatalf("tracker comments = %+v, want the superseded note on the retired parent alone", fake.comments)
	}
}

func TestGrillApplyCreateInternalAnchorToTrackerFilesFresh(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "New epic",
		ProposedDescription: "Epic body.",
		Summary:             "one slice",
		SubIssues:           []grillSubIssue{{Title: "S1", Description: "d1"}},
	})

	// Pass one files internally and fails on a nameless slice the store refuses,
	// leaving the session finished on the internal parent.
	_, out := applyGrill(t, ts, sid, GrillApplyRequest{
		Destination: "internal",
		SubIssues:   []grillSubIssue{{Title: "", Description: "nameless"}},
	})
	if out.Applied || out.Session.IssueID != "ACME-1" {
		t.Fatalf("internal apply = %+v, want the internal parent anchored and the session left finished", out)
	}

	fake.createQueue = []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-300"}},
		{issue: tracker.NewIssue{Identifier: "COD-301"}},
	}
	_, out = applyGrill(t, ts, sid, GrillApplyRequest{Destination: "tracker"})
	if !out.Applied {
		t.Fatalf("tracker re-apply = %+v, want applied", out)
	}
	if hasDetachStep(out.Steps) {
		t.Errorf("steps = %+v, want no conversion — promoting an internal issue to a tracker is a separate feature", out.Steps)
	}
	if out.Session.IssueID != "COD-300" || out.Session.IssueDestination != grillDestTracker {
		t.Errorf("session = %s/%s, want the freshly filed tracker parent",
			out.Session.IssueID, out.Session.IssueDestination)
	}
	if _, found, err := stores.Issues().Internal(root, "ACME-1"); err != nil || !found {
		t.Errorf("internal parent: found=%v err=%v, want it left exactly as it was", found, err)
	}
	for _, call := range fake.comments {
		if strings.Contains(call.body, "Superseded by") {
			t.Errorf("tracker comment = %+v, want no superseded note when nothing was converted", call)
		}
	}
}
