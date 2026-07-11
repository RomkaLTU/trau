package webserver

import (
	"context"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// TestInternalProviderDrivesIssuesThroughTheHub exercises the whole chain
// end-to-end: the internal tracker provider talking hubclient → HTTP → the hub's
// handlers → the issue store, with no database access from the provider side. It
// covers pick, the In Progress / Done status transitions the pipeline makes, and
// the quarantine (needs-human) failure class.
func TestInternalProviderDrivesIssuesThroughTheHub(t *testing.T) {
	ts, root, store := internalIssueServer(t, false)
	ready := "ready-for-agent"
	quarantine := "needs-human"

	if _, err := store.CreateInternal(root, "ACME", hubstore.InternalDraft{
		Title: "Add search", State: "unstarted", Labels: []string{ready},
	}); err != nil {
		t.Fatalf("seed ready issue: %v", err)
	}
	// A started issue and an epic must both be skipped by pick.
	if _, err := store.CreateInternal(root, "ACME", hubstore.InternalDraft{Title: "In flight", State: "started", Labels: []string{ready}}); err != nil {
		t.Fatalf("seed started issue: %v", err)
	}

	pm, err := tracker.New("internal", nil, tracker.Config{
		Repo: "acme", HubBaseURL: ts.URL, ReadyLabel: ready, QuarantineLabel: quarantine,
	})
	if err != nil {
		t.Fatalf("build internal tracker: %v", err)
	}
	ctx := context.Background()

	id, err := pm.Pick(ctx, tracker.Scope{})
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if id != "ACME-1" {
		t.Fatalf("pick = %q, want ACME-1 (the only ready, unstarted leaf)", id)
	}

	if err := pm.SetStatus(ctx, id, "In Progress", ""); err != nil {
		t.Fatalf("set in progress: %v", err)
	}
	if got, _, _ := store.Internal(root, id); got.StatusGroup != "started" {
		t.Fatalf("status after In Progress = %q, want started", got.StatusGroup)
	}

	if err := pm.SetStatus(ctx, id, "Done", ""); err != nil {
		t.Fatalf("set done: %v", err)
	}
	if got, _, _ := store.Internal(root, id); got.StatusGroup != "done" {
		t.Fatalf("status after Done = %q, want done", got.StatusGroup)
	}

	if err := pm.Quarantine(ctx, id, "verify dead end"); err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	got, _, _ := store.Internal(root, id)
	if hasLabel(got.Labels, ready) || !hasLabel(got.Labels, quarantine) {
		t.Fatalf("labels after quarantine = %v, want the ready label dropped and %q added", got.Labels, quarantine)
	}
	listed, err := store.List(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) == 0 || len(listed[0].Comments) == 0 {
		t.Fatalf("want a quarantine comment recorded on the issue, got %+v", listed)
	}
}
