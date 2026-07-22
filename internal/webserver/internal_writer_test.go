package webserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// grillApplyInternalServer builds a server whose repo runs the internal tracker
// provider, so an apply exercises the store-backed writer — the injected factory
// must never be reached.
func grillApplyInternalServer(t *testing.T) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	ts, stores, root := grillApplyServerWriter(t, func(config.Config) (tracker.Writer, error) {
		t.Error("internal repo built an external writer")
		return nil, errString("unexpected external writer")
	})
	ini := "TRACKER_PROVIDER=internal\nISSUE_PREFIX=ACME\n"
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte(ini), 0o644); err != nil {
		t.Fatalf("write repo config: %v", err)
	}
	return ts, stores, root
}

func TestGrillApplyInternalCreateSingle(t *testing.T) {
	ts, stores, root := grillApplyInternalServer(t)
	sid := seedFinishedGrill(t, stores, root, "", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "Add dark mode toggle",
		ProposedDescription: "As a user I can toggle dark mode.",
		Summary:             "specced the toggle",
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK || !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("apply = %+v (status %d), want applied", out, res.StatusCode)
	}
	if out.Session.IssueID != "ACME-1" {
		t.Errorf("session anchor = %q, want the allocated internal id ACME-1", out.Session.IssueID)
	}
	for _, step := range out.Steps {
		if step.Status != grillStepOK {
			t.Errorf("step %s = %+v, want ok", step.Step, step)
		}
	}
	iss, found, err := stores.Issues().Find(root, "ACME-1")
	if err != nil || !found {
		t.Fatalf("stored issue: found=%v err=%v", found, err)
	}
	if iss.Source != hubstore.SourceInternal {
		t.Errorf("source = %q, want internal", iss.Source)
	}
	if iss.Title != "Add dark mode toggle" || iss.Description != "As a user I can toggle dark mode." {
		t.Errorf("stored issue = %q / %q, want the applied content", iss.Title, iss.Description)
	}
	if !hasLabel(iss.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want the default ready label", iss.Labels)
	}
	// The post-create mirror must not clobber what CreateInternal wrote.
	if iss.StatusGroup != "backlog" || iss.Status != "Backlog" {
		t.Errorf("state = %s/%s, want the created backlog state intact", iss.StatusGroup, iss.Status)
	}
	if len(iss.Comments) != 1 || !strings.Contains(iss.Comments[0].Body, "Grilling summary") {
		t.Errorf("comments = %+v, want the grilling summary appended", iss.Comments)
	}
}

func TestGrillApplyInternalCreateEpic(t *testing.T) {
	ts, stores, root := grillApplyInternalServer(t)
	sid := seedFinishedGrill(t, stores, root, "", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "Checkout redesign",
		ProposedDescription: "Epic: redesign the checkout.",
		Summary:             "authored an epic",
		SubIssues: []grillSubIssue{
			{Title: "Cart page", Description: "rebuild the cart"},
			{Title: "Payment", Description: "wire the payment step"},
		},
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK || !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("apply = %+v (status %d), want applied", out, res.StatusCode)
	}
	parent, found, err := stores.Issues().Get(root, "ACME-1")
	if err != nil || !found {
		t.Fatalf("stored parent: found=%v err=%v", found, err)
	}
	if parent.Source != hubstore.SourceInternal || !parent.HasChildren {
		t.Errorf("parent = source %q has_children %v, want an internal epic", parent.Source, parent.HasChildren)
	}
	kids, err := stores.Issues().Children(root, "ACME-1")
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(kids) != 2 {
		t.Fatalf("children = %d, want 2", len(kids))
	}
	for _, k := range kids {
		if k.Source != hubstore.SourceInternal || k.Parent != "ACME-1" {
			t.Errorf("child %s = source %q parent %q, want internal under ACME-1", k.Identifier, k.Source, k.Parent)
		}
		if !hasLabel(k.Labels, "ready-for-agent") {
			t.Errorf("child %s labels = %v, want the default ready label", k.Identifier, k.Labels)
		}
	}
}

func TestGrillApplyInternalEpicPersistsRelations(t *testing.T) {
	ts, stores, root := grillApplyInternalServer(t)
	sid := seedFinishedGrill(t, stores, root, "", grillOutcome{
		Disposition:         grillDispCreate,
		Title:               "Checkout redesign",
		ProposedDescription: "Epic body.",
		Summary:             "two slices",
		SubIssues: []grillSubIssue{
			{Title: "S1", Description: "d1"},
			{Title: "S2", Description: "d2", BlockedBy: []int{0}},
		},
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK || !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("apply = %+v (status %d), want applied", out, res.StatusCode)
	}
	if st, ok := stepStatus(out.Steps, "relations"); !ok || st != grillStepOK {
		t.Fatalf("relations step = %q (present %v), want ok", st, ok)
	}
	blockers, err := stores.Issues().Blockers(root, "ACME-3")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0] != "ACME-2" {
		t.Fatalf("blockers of ACME-3 = %v, want the S1 slice ACME-2", blockers)
	}
	iss, found, err := stores.Issues().Find(root, "ACME-3")
	if err != nil || !found {
		t.Fatalf("find ACME-3: found=%v err=%v", found, err)
	}
	if !iss.Blocked {
		t.Fatalf("blocked = false, want ACME-3 held back while ACME-2 is open")
	}
	if err := stores.Issues().AddRelation(root, "ACME-2", "ACME-3"); err != nil {
		t.Fatalf("re-add relation: %v", err)
	}
	if blockers, err = stores.Issues().Blockers(root, "ACME-3"); err != nil || len(blockers) != 1 {
		t.Fatalf("blockers after re-add = %v (err %v), want still exactly one", blockers, err)
	}
}

func TestGrillApplyInternalRewrite(t *testing.T) {
	ts, stores, root := grillApplyInternalServer(t)
	if _, err := stores.Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{
		Title:       "Unclear ticket",
		Description: "stale one-liner",
		Labels:      []string{"needs-triage"},
	}); err != nil {
		t.Fatalf("seed internal issue: %v", err)
	}
	sid := seedFinishedGrill(t, stores, root, "ACME-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "fully specified body",
		Summary:             "clarified",
	})

	if _, out := applyGrill(t, ts, sid, GrillApplyRequest{}); !out.Applied {
		t.Fatalf("apply = %+v, want applied", out)
	}
	iss, found, err := stores.Issues().Find(root, "ACME-1")
	if err != nil || !found {
		t.Fatalf("stored issue: found=%v err=%v", found, err)
	}
	if iss.Description != "fully specified body" {
		t.Errorf("description = %q, want the rewritten body", iss.Description)
	}
	if iss.Title != "Unclear ticket" {
		t.Errorf("title = %q, want it untouched by the description update", iss.Title)
	}
	if hasLabel(iss.Labels, "needs-triage") || !hasLabel(iss.Labels, "ready-for-agent") {
		t.Errorf("labels = %v, want needs-triage swapped for ready-for-agent", iss.Labels)
	}
	if len(iss.Comments) != 1 {
		t.Errorf("comments = %+v, want the grilling summary appended", iss.Comments)
	}
}

func TestInternalWriterRefusesDocuments(t *testing.T) {
	w := &internalWriter{}
	if _, err := w.PublishDocument(context.Background(), tracker.DocumentDraft{Title: "PRD", Markdown: "body"}); err == nil {
		t.Fatal("PublishDocument on internal = nil error, want an explicit refusal")
	}
}

func TestInternalWriterRefusesAssignment(t *testing.T) {
	w := &internalWriter{}
	if err := w.AssignIssue(context.Background(), "ACME-1", "u-1"); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("AssignIssue on internal = %v, want ErrUnsupported", err)
	}
	if _, err := w.AssignableUsers(context.Background(), ""); !errors.Is(err, tracker.ErrUnsupported) {
		t.Errorf("AssignableUsers on internal = %v, want ErrUnsupported", err)
	}
}
