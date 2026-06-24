package pipeline

import (
	"context"
	"errors"
	"testing"
)

// projectTracker is a fakeTracker that also answers IssueProject, so it satisfies
// tracker.IssueProjecter for the ownership guard.
type projectTracker struct {
	*fakeTracker
	project string
	err     error
}

func (t projectTracker) IssueProject(context.Context, string) (string, error) {
	return t.project, t.err
}

func TestEnsureOwnedProject(t *testing.T) {
	tests := []struct {
		name    string
		owned   string
		project string
		err     error
		wantX   bool // expect a CrossProjectError refusal
	}{
		{"no owned project — guard off", "", "trau", nil, false},
		{"mismatch refused", "salonradar.com", "trau", nil, true},
		{"match allowed", "salonradar.com", "salonradar.com", nil, false},
		{"case-insensitive match", "Salonradar.com", "salonradar.com", nil, false},
		{"unknown project — can't enforce", "salonradar.com", "", nil, false},
		{"lookup error — can't enforce", "salonradar.com", "trau", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{
				OwnedProject: tc.owned,
				Tracker:      projectTracker{fakeTracker: &fakeTracker{}, project: tc.project, err: tc.err},
			}
			err := p.EnsureOwnedProject(context.Background(), "COD-582")
			if IsCrossProject(err) != tc.wantX {
				t.Fatalf("EnsureOwnedProject err=%v, want CrossProject=%v", err, tc.wantX)
			}
			if !tc.wantX && err != nil {
				t.Fatalf("EnsureOwnedProject = %v, want nil", err)
			}
		})
	}
}

// A tracker that does NOT implement IssueProjecter makes the guard a no-op even
// when an owned project is set — uncertainty never blocks a run.
func TestEnsureOwnedProjectNoProjecter(t *testing.T) {
	p := &Pipeline{OwnedProject: "salonradar.com", Tracker: &fakeTracker{}}
	if err := p.EnsureOwnedProject(context.Background(), "COD-582"); err != nil {
		t.Fatalf("EnsureOwnedProject with non-projecter tracker = %v, want nil", err)
	}
}
