package hubstore

import (
	"errors"
	"reflect"
	"testing"
)

func mustSetTracker(t *testing.T, p *Projects, id string, keys map[string]string) {
	t.Helper()
	if err := p.SetTracker(id, keys); err != nil {
		t.Fatalf("SetTracker(%q): %v", id, err)
	}
}

func trackerOf(t *testing.T, p *Projects, id string) map[string]string {
	t.Helper()
	keys, err := p.Tracker(id)
	if err != nil {
		t.Fatalf("Tracker(%q): %v", id, err)
	}
	return keys
}

func seededOf(t *testing.T, p *Projects, root string) map[string]bool {
	t.Helper()
	keys, err := p.SeededTrackerKeys(root)
	if err != nil {
		t.Fatalf("SeededTrackerKeys(%q): %v", root, err)
	}
	return keys
}

func TestSetTrackerReplacesEveryStoredKey(t *testing.T) {
	p := testProjects(t, t.TempDir())
	proj := mustCreateProject(t, p, "Platform")

	if got := trackerOf(t, p, proj.ID); len(got) != 0 {
		t.Fatalf("new project tracker = %v, want empty", got)
	}

	mustSetTracker(t, p, proj.ID, map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": "COD"})
	want := map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": "COD"}
	if got := trackerOf(t, p, proj.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("tracker = %v, want %v", got, want)
	}

	mustSetTracker(t, p, proj.ID, map[string]string{"TRACKER_PROVIDER": "internal"})
	want = map[string]string{"TRACKER_PROVIDER": "internal"}
	if got := trackerOf(t, p, proj.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("tracker after replace = %v, want %v", got, want)
	}
}

func TestSetTrackerRejectsAnUnknownProject(t *testing.T) {
	p := testProjects(t, t.TempDir())

	err := p.SetTracker("ghost", map[string]string{"TRACKER_PROVIDER": "linear"})
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("SetTracker on a missing project = %v, want ErrProjectNotFound", err)
	}
}

func TestMarkTrackerSeededReplacesTheRootsKeys(t *testing.T) {
	p := testProjects(t, t.TempDir())

	if err := p.MarkTrackerSeeded("/repos/api", []string{"TRACKER_PROVIDER", "LINEAR_TEAM"}); err != nil {
		t.Fatalf("MarkTrackerSeeded: %v", err)
	}
	want := map[string]bool{"TRACKER_PROVIDER": true, "LINEAR_TEAM": true}
	if got := seededOf(t, p, "/repos/api"); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded = %v, want %v", got, want)
	}

	if err := p.MarkTrackerSeeded("/repos/api", []string{"TRACKER_PROVIDER"}); err != nil {
		t.Fatalf("second MarkTrackerSeeded: %v", err)
	}
	want = map[string]bool{"TRACKER_PROVIDER": true}
	if got := seededOf(t, p, "/repos/api"); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded after replace = %v, want %v", got, want)
	}
}

// A repo leaving a project keeps the values in its config file, but they are its
// own from then on — nothing may overwrite them behind its back.
func TestLeavingAProjectHandsSeededKeysBackToTheRepo(t *testing.T) {
	cases := []struct {
		name  string
		leave func(t *testing.T, p *Projects, id, root string)
	}{
		{"removed from the project", func(t *testing.T, p *Projects, id, root string) {
			if _, err := p.RemoveRepo(id, root); err != nil {
				t.Fatalf("RemoveRepo: %v", err)
			}
		}},
		{"forgotten by the hub", func(t *testing.T, p *Projects, id, root string) {
			if err := p.ForgetRoot(root); err != nil {
				t.Fatalf("ForgetRoot: %v", err)
			}
		}},
		{"project deleted", func(t *testing.T, p *Projects, id, root string) {
			if err := p.Delete(id); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testProjects(t, t.TempDir())
			proj := mustCreateProject(t, p, "Platform")
			mustAddRepo(t, p, proj.ID, "/repos/api")
			mustAddRepo(t, p, proj.ID, "/repos/web")
			if err := p.MarkTrackerSeeded("/repos/api", []string{"TRACKER_PROVIDER"}); err != nil {
				t.Fatalf("MarkTrackerSeeded: %v", err)
			}

			tc.leave(t, p, proj.ID, "/repos/api")

			if got := seededOf(t, p, "/repos/api"); len(got) != 0 {
				t.Fatalf("seeded after leaving = %v, want empty", got)
			}
		})
	}
}

func TestDeletingAProjectDropsItsTracker(t *testing.T) {
	p := testProjects(t, t.TempDir())
	proj := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, proj.ID, "/repos/api")
	mustSetTracker(t, p, proj.ID, map[string]string{"TRACKER_PROVIDER": "linear"})

	if err := p.Delete(proj.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	again := mustCreateProject(t, p, "Platform")
	if again.ID != proj.ID {
		t.Fatalf("recreated project id = %q, want the freed %q", again.ID, proj.ID)
	}
	if got := trackerOf(t, p, again.ID); len(got) != 0 {
		t.Fatalf("recreated project tracker = %v, want empty", got)
	}
}

// A project emptied by its last member moving away is pruned, so its tracker must
// not outlive it and greet whatever reclaims the slug.
func TestPrunedProjectDropsItsTracker(t *testing.T) {
	p := testProjects(t, t.TempDir())
	lone := mustCreateProject(t, p, "api")
	mustAddRepo(t, p, lone.ID, "/repos/api")
	mustSetTracker(t, p, lone.ID, map[string]string{"TRACKER_PROVIDER": "jira"})

	group := mustCreateProject(t, p, "Platform")
	mustAddRepo(t, p, group.ID, "/repos/api")

	reclaimed := mustCreateProject(t, p, "api")
	if reclaimed.ID != lone.ID {
		t.Fatalf("reclaimed project id = %q, want the freed %q", reclaimed.ID, lone.ID)
	}
	if got := trackerOf(t, p, reclaimed.ID); len(got) != 0 {
		t.Fatalf("reclaimed project tracker = %v, want empty", got)
	}
}
