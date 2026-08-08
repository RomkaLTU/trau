package webserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
)

// epicLaneServer is laneServer over a real one-commit git repository, so the trees
// this slice provisions and settles are trees git actually made and removed.
func epicLaneServer(t *testing.T, lanes string) (*Server, *fakeSupervisor, string, string) {
	t.Helper()
	s, fake, root := drainServer(t, "acme")
	gitCmd(t, root, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "add", "-A")
	gitCmd(t, root, "commit", "-m", "init")

	trees := filepath.Join(t.TempDir(), "worktrees")
	writeRepoINI(t, root, "LINEAR_TEAM=COD\nWORKTREES=1\nWORKTREES_DIR="+trees+
		"\nWORKTREE_PARALLEL="+lanes+"\n")
	live := map[int]bool{}
	s.drain.alive = func(pid int) bool { return live[pid] }
	fake.onSpawn = func(pid int) { live[pid] = true }
	return s, fake, root, trees
}

// epicSubs is a two-child epic, the smallest shape whose children have to take
// turns in one tree.
func epicSubs() []queue.SubIssue {
	return []queue.SubIssue{{ID: "COD-91"}, {ID: "COD-92"}}
}

// TestAnEpicTakesOneLaneBesideTicketLanes is the slice's headline: on a repo with
// worktrees an epic no longer freezes the queue. It starts as one child in one
// tree — keyed by the epic, so every sub-issue and the finalize share it — while
// the ordinary tickets run their own lanes beside it.
func TestAnEpicTakesOneLaneBesideTicketLanes(t *testing.T) {
	s, fake, root, trees := epicLaneServer(t, "3")
	seedQueue(t, s, root, true,
		queue.Item{Kind: queue.KindEpic, ID: "COD-9", SubIssues: epicSubs()},
		queue.Item{ID: "COD-1"}, queue.Item{ID: "COD-2"},
	)

	for i := 1; i <= 3; i++ {
		act, err := s.drain.tick(root)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if act != drainSpawn {
			t.Fatalf("tick %d = %q, want %q — the epic holds one lane, not the repo", i, act, drainSpawn)
		}
	}
	if len(fake.spawns) != 3 {
		t.Fatalf("spawned %d children, want the epic plus both tickets", len(fake.spawns))
	}

	want := map[string]string{
		"COD-9": filepath.Join(trees, "acme", "COD-9"),
		"COD-1": filepath.Join(trees, "acme", "COD-1"),
		"COD-2": filepath.Join(trees, "acme", "COD-2"),
	}
	for _, spec := range fake.spawns {
		id := flagValue(spec.Args, "--parent")
		if got := flagValue(spec.Args, "--worktree"); got != want[id] {
			t.Errorf("%s runs in %q, want %q", id, got, want[id])
		}
	}
	for _, spec := range fake.spawns {
		if flagValue(spec.Args, "--parent") != "COD-9" {
			continue
		}
		for _, arg := range spec.Args {
			if arg == "--once" {
				t.Error("the epic child was launched with --once — it has to run every sub-issue and the finalize")
			}
		}
	}
	for _, id := range []string{"COD-9", "COD-1", "COD-2"} {
		if got := statusOf(t, s, root, id); got != queue.StatusRunning {
			t.Errorf("%s = %q, want it running in its own lane", id, got)
		}
	}
}

// TestAReleasingEpicHoldsOnlyItsOwnLane covers the gate this slice narrows: with
// worktrees the merge happens in the epic's tree, so the other lanes keep starting
// and the epic's own finalize is the only run that re-enters its lane — whichever
// way round the queue holds them.
func TestAReleasingEpicHoldsOnlyItsOwnLane(t *testing.T) {
	epic := queue.Item{Kind: queue.KindEpic, ID: "COD-9", Status: queue.StatusPaused,
		Reason: "outcome unknown", SubIssues: epicSubs()}
	ticket := queue.Item{ID: "COD-1"}
	tests := []struct {
		name  string
		items []queue.Item
	}{
		{name: "release first in run order", items: []queue.Item{epic, ticket}},
		{name: "release behind a pending ticket", items: []queue.Item{ticket, epic}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, fake, root, trees := epicLaneServer(t, "3")
			seedReleasing(t, s, root, "COD-9", state.ReleaseActive)
			seedQueue(t, s, root, true, tc.items...)

			for i := 1; i <= 2; i++ {
				if act, err := s.drain.tick(root); err != nil || act != drainSpawn {
					t.Fatalf("tick %d = %q (%v), want both lanes to start", i, act, err)
				}
			}
			started := map[string]string{}
			for _, spec := range fake.spawns {
				started[flagValue(spec.Args, "--parent")] = flagValue(spec.Args, "--worktree")
			}
			if _, ok := started["COD-1"]; !ok {
				t.Error("the ticket never started — a release must hold the epic's lane only")
			}
			tree, ok := started["COD-9"]
			if !ok {
				t.Fatal("the releasing epic's own finalize never re-entered its lane")
			}
			if want := filepath.Join(trees, "acme", "COD-9"); tree != want {
				t.Errorf("the release re-entered %q, want the epic's existing tree %q", tree, want)
			}
			if got := queueOf(t, s, root).ReleasingEpic; got != "" {
				t.Errorf("releasing_epic = %q, want it empty — nothing in this repo is held by the release", got)
			}
		})
	}
}

// TestASharedCheckoutStillFreezesOnARelease is the other half of the same gate: a
// repo whose runs share one checkout is mid-merge in that checkout while an epic
// releases, so nothing else may start and the board is told which epic to wait for.
func TestASharedCheckoutStillFreezesOnARelease(t *testing.T) {
	s, fake, root := drainServer(t, "acme")
	seedReleasing(t, s, root, "COD-9", state.ReleaseActive)
	seedQueue(t, s, root, true, queue.Item{ID: "COD-1"},
		queue.Item{Kind: queue.KindEpic, ID: "COD-9", Status: queue.StatusPaused, SubIssues: epicSubs()})

	act, err := s.drain.tick(root)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if act != drainSpawn {
		t.Fatalf("tick = %q, want the releasing epic's finalize to be the one thing let through", act)
	}
	if got := flagValue(fake.spawns[0].Args, "--parent"); got != "COD-9" {
		t.Fatalf("spawned %q, want the epic whose release holds the repo", got)
	}
	if got := statusOf(t, s, root, "COD-1"); got != queue.StatusPending {
		t.Errorf("COD-1 = %q, want it still waiting behind the release", got)
	}
	if got := queueOf(t, s, root).ReleasingEpic; got != "COD-9" {
		t.Errorf("releasing_epic = %q, want COD-9 — the whole repo is held", got)
	}
}

// TestAnEpicMergeGivesUpItsTreeAndAHandOffKeepsIt pins the settle grain: a shipped
// epic's tree goes with the row, while a release handed to a human keeps its tree
// for as long as the PR is theirs to land — and gives it up the moment the
// reconcile sweep sees the merge.
func TestAnEpicMergeGivesUpItsTreeAndAHandOffKeepsIt(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint map[string]string
		outcome    string
		wantStatus string
		wantTree   bool
	}{
		{
			name:       "merged",
			checkpoint: map[string]string{"PHASE": state.Merged, "PR": "42"},
			wantStatus: queue.StatusDone,
		},
		{
			name:       "handed to a human",
			checkpoint: map[string]string{"PHASE": state.Releasing, "RELEASE": state.ReleaseAwaitingHuman, "PR": "42"},
			outcome:    state.FailAwaitingMerge,
			wantStatus: queue.StatusAwaitingMerge,
			wantTree:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root, trees := epicLaneServer(t, "3")
			repo := registry.Repo{Name: "acme", Root: root}
			tree := addWorktree(t, repo, trees, "COD-9")
			if _, err := s.stores.Worktrees().Record(root, hubstore.WorktreeInput{
				Ticket: "COD-9", Path: tree, State: hubstore.WorktreeActive,
			}); err != nil {
				t.Fatalf("record the epic's worktree: %v", err)
			}
			if err := s.stores.Checkpoints().Upsert(root, "COD-9", tc.checkpoint); err != nil {
				t.Fatalf("seed checkpoint: %v", err)
			}
			s.drain.outcome = func(string, queue.Item) (string, string) { return tc.outcome, tc.outcome }
			seedQueue(t, s, root, true, queue.Item{
				Kind: queue.KindEpic, ID: "COD-9", Status: queue.StatusRunning, PID: 4242, SubIssues: epicSubs(),
			})

			if act, err := s.drain.tick(root); err != nil || act != drainReconcile {
				t.Fatalf("tick = %q (%v), want the dead epic reconciled", act, err)
			}
			if got := statusOf(t, s, root, "COD-9"); got != tc.wantStatus {
				t.Fatalf("COD-9 = %q, want %q", got, tc.wantStatus)
			}
			_, err := os.Stat(tree)
			if tc.wantTree && err != nil {
				t.Fatalf("the hand-off gave up its tree: %v — it stays while a human owns the merge", err)
			}
			if !tc.wantTree && !os.IsNotExist(err) {
				t.Fatalf("the shipped epic kept its tree (err = %v)", err)
			}
			if !tc.wantTree {
				return
			}

			// The PR lands: the sweep settles the row on that evidence, and the tree
			// goes with it.
			s.drain.prState = func(string, string) string { return "MERGED" }
			s.drain.reconcileQueue(root)

			if got := statusOf(t, s, root, "COD-9"); got != queue.StatusDone {
				t.Fatalf("COD-9 = %q, want done once its PR merged", got)
			}
			if _, err := os.Stat(tree); !os.IsNotExist(err) {
				t.Errorf("the tree outlived the merged epic PR (err = %v)", err)
			}
			row, found, err := s.stores.Worktrees().ByTicket(root, "COD-9")
			if err != nil || !found {
				t.Fatalf("read the worktree row: %v (found %v)", err, found)
			}
			if row.State != hubstore.WorktreeSettled {
				t.Errorf("worktree row = %q, want it settled", row.State)
			}
		})
	}
}
