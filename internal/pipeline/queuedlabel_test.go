package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// labelingTracker is a tracker that can also drop a single label, recording each
// removal.
type labelingTracker struct {
	fakeTracker
	removed []string
	err     error
}

func (t *labelingTracker) RemoveLabel(_ context.Context, id, label string) error {
	t.removed = append(t.removed, id+"/"+label)
	return t.err
}

// The In Progress seam is where an epic's slice sheds the hub queue's label.
func TestResolveBuildBranchClearsQueuedLabel(t *testing.T) {
	cases := []struct {
		name    string
		label   string
		err     error
		wantRm  []string
		wantErr bool
	}{
		{name: "configured label is removed", label: "queued", wantRm: []string{"COD-7/queued"}},
		{name: "empty label writes nothing", label: "", wantRm: nil},
		{name: "failed removal does not stop the run", label: "queued", err: errors.New("linear said no"), wantRm: []string{"COD-7/queued"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &labelingTracker{err: tc.err}
			p := newTestPipeline(t, fakeRunner{}, tr)
			p.QueuedLabel = tc.label

			branch, err := p.resolveBuildBranch(context.Background(), "COD-7")
			if err != nil {
				t.Fatalf("resolveBuildBranch err = %v", err)
			}
			if branch != "feature/COD-7" {
				t.Errorf("branch = %q, want feature/COD-7", branch)
			}
			if !reflect.DeepEqual(tr.removed, tc.wantRm) {
				t.Errorf("removals = %v, want %v", tr.removed, tc.wantRm)
			}
		})
	}
}

// A tracker with no label-removal capability leaves the seam a no-op.
func TestResolveBuildBranchWithoutLabelCapability(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.QueuedLabel = "queued"

	if _, err := p.resolveBuildBranch(context.Background(), "COD-7"); err != nil {
		t.Fatalf("resolveBuildBranch err = %v", err)
	}
}
