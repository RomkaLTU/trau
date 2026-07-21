package hubstore

import (
	"reflect"
	"testing"
)

func seedSyncedInto(t *testing.T, store *Issues, repo string, issues ...Issue) {
	t.Helper()
	if _, _, err := store.Upsert(repo, "linear", issues); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func TestAddRelationIdempotentReadsBothDirections(t *testing.T) {
	store := testIssues(t)
	for _, edge := range [][2]string{{"ACME-1", "ACME-2"}, {"ACME-1", "ACME-2"}, {"ACME-1", "ACME-3"}} {
		if err := store.AddRelation("acme", edge[0], edge[1]); err != nil {
			t.Fatalf("add %v: %v", edge, err)
		}
	}
	blockers, err := store.Blockers("acme", "ACME-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if !reflect.DeepEqual(blockers, []string{"ACME-1"}) {
		t.Errorf("blockers of ACME-2 = %v, want [ACME-1] with no duplicate from the re-add", blockers)
	}
	deps, err := store.Dependents("acme", "ACME-1")
	if err != nil {
		t.Fatalf("dependents: %v", err)
	}
	if !reflect.DeepEqual(deps, []string{"ACME-2", "ACME-3"}) {
		t.Errorf("dependents of ACME-1 = %v, want [ACME-2 ACME-3]", deps)
	}
}

func TestAddRelationRejectsBlankAndSelf(t *testing.T) {
	store := testIssues(t)
	if err := store.AddRelation("acme", "", "ACME-2"); err == nil {
		t.Error("blank blocker accepted, want an error")
	}
	if err := store.AddRelation("acme", "ACME-2", "ACME-2"); err == nil {
		t.Error("self-blocking edge accepted, want an error")
	}
}

func TestRemoveRelation(t *testing.T) {
	store := testIssues(t)
	if err := store.AddRelation("acme", "ACME-1", "ACME-2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.RemoveRelation("acme", "ACME-1", "ACME-2"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := store.RemoveRelation("acme", "ACME-1", "ACME-2"); err != nil {
		t.Fatalf("remove absent edge: %v", err)
	}
	blockers, err := store.Blockers("acme", "ACME-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("blockers = %v, want none after removal", blockers)
	}
}

func TestReflectBlockersSurvivesResync(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "unstarted"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	refs := []BlockerRef{{ID: "ENG-1"}}
	for i := 0; i < 2; i++ {
		if err := store.ReflectBlockers("acme", "ENG-2", refs); err != nil {
			t.Fatalf("reflect %d: %v", i, err)
		}
	}
	blockers, err := store.Blockers("acme", "ENG-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if !reflect.DeepEqual(blockers, []string{"ENG-1"}) {
		t.Fatalf("blockers = %v, want [ENG-1] intact across re-syncs", blockers)
	}
	if err := store.ReflectBlockers("acme", "ENG-2", nil); err != nil {
		t.Fatalf("reflect empty: %v", err)
	}
	blockers, err = store.Blockers("acme", "ENG-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("blockers = %v, want the dropped link gone", blockers)
	}
}

func TestReflectBlockersKeepsDanglingUnresolvedSkipsResolvedUnknown(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "done blocker", StatusGroup: "done"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	err := store.ReflectBlockers("acme", "ENG-2", []BlockerRef{
		{ID: "ENG-1", Resolved: true},
		{ID: "OTHER-9", Resolved: false},
		{ID: "OTHER-8", Resolved: true},
	})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	blockers, err := store.Blockers("acme", "ENG-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if !reflect.DeepEqual(blockers, []string{"ENG-1", "OTHER-9"}) {
		t.Errorf("blockers = %v, want the stored blocker and the unresolved dangling one, not the resolved unknown", blockers)
	}
}

func TestReflectBlockersOnlyTouchesSyncedRows(t *testing.T) {
	store := testIssues(t)
	if _, err := store.CreateInternal("acme", "ACME", InternalDraft{Title: "internal"}); err != nil {
		t.Fatalf("create internal: %v", err)
	}
	if err := store.ReflectBlockers("acme", "ACME-1", []BlockerRef{{ID: "ENG-1"}}); err != nil {
		t.Fatalf("reflect onto internal: %v", err)
	}
	if err := store.ReflectBlockers("acme", "ENG-404", []BlockerRef{{ID: "ENG-1"}}); err != nil {
		t.Fatalf("reflect onto missing: %v", err)
	}
	for _, id := range []string{"ACME-1", "ENG-404"} {
		blockers, err := store.Blockers("acme", id)
		if err != nil {
			t.Fatalf("blockers %s: %v", id, err)
		}
		if len(blockers) != 0 {
			t.Errorf("blockers of %s = %v, want inbound reflection to skip non-synced targets", id, blockers)
		}
	}
}

func TestFindResolvesBlockedFromBlockerState(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "unstarted"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	if err := store.AddRelation("acme", "ENG-1", "ENG-2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := store.AddRelation("acme", "GHOST-1", "ENG-1"); err != nil {
		t.Fatalf("add dangling: %v", err)
	}

	iss, _, err := store.Find("acme", "ENG-2")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !iss.Blocked || !reflect.DeepEqual(iss.Blockers, []string{"ENG-1"}) {
		t.Errorf("ENG-2 = blocked %v blockers %v, want blocked by the live ENG-1", iss.Blocked, iss.Blockers)
	}

	iss, _, err = store.Find("acme", "ENG-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !iss.Blocked {
		t.Errorf("ENG-1 blocked = false, want a dangling blocker to count as unresolved")
	}

	seedSyncedInto(t, store, "acme", Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "done"})
	iss, _, err = store.Find("acme", "ENG-2")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.Blocked || !reflect.DeepEqual(iss.Blockers, []string{"ENG-1"}) {
		t.Errorf("ENG-2 = blocked %v blockers %v, want unblocked once ENG-1 is done, edge kept", iss.Blocked, iss.Blockers)
	}
}

func TestBlockedIgnoresTombstonedBlocker(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "unstarted"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	if err := store.AddRelation("acme", "ENG-1", "ENG-2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := store.Reconcile("acme", []string{"ENG-2"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	iss, _, err := store.Find("acme", "ENG-2")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if iss.Blocked {
		t.Errorf("blocked = true, want a tracker-removed blocker to no longer hold ENG-2 back")
	}
}

func TestBacklogPageCarriesBlockers(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "unstarted"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	if err := store.AddRelation("acme", "ENG-1", "ENG-2"); err != nil {
		t.Fatalf("add: %v", err)
	}
	issues, _, _, err := store.BacklogPage("acme", BacklogFilter{})
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	byID := map[string]Issue{}
	for _, iss := range issues {
		byID[iss.Identifier] = iss
	}
	if got := byID["ENG-2"]; !got.Blocked || !reflect.DeepEqual(got.Blockers, []string{"ENG-1"}) {
		t.Errorf("ENG-2 = blocked %v blockers %v, want blocked by ENG-1", got.Blocked, got.Blockers)
	}
	if got := byID["ENG-1"]; got.Blocked || len(got.Blockers) != 0 {
		t.Errorf("ENG-1 = blocked %v blockers %v, want unblocked", got.Blocked, got.Blockers)
	}
}

func TestDropSyncedCleansSyncedEdgesKeepsInternal(t *testing.T) {
	store := testIssues(t)
	seedSyncedInto(t, store, "acme",
		Issue{Identifier: "ENG-1", Title: "blocker", StatusGroup: "unstarted"},
		Issue{Identifier: "ENG-2", Title: "dependent", StatusGroup: "unstarted"},
	)
	if _, err := store.CreateInternal("acme", "ACME", InternalDraft{Title: "internal"}); err != nil {
		t.Fatalf("create internal: %v", err)
	}
	if err := store.AddRelation("acme", "ENG-1", "ENG-2"); err != nil {
		t.Fatalf("add synced edge: %v", err)
	}
	if err := store.AddRelation("acme", "ENG-1", "ACME-1"); err != nil {
		t.Fatalf("add internal edge: %v", err)
	}
	if err := store.DropSynced("acme"); err != nil {
		t.Fatalf("drop synced: %v", err)
	}
	blockers, err := store.Blockers("acme", "ENG-2")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("blockers of ENG-2 = %v, want the edge dropped with its issue", blockers)
	}
	blockers, err = store.Blockers("acme", "ACME-1")
	if err != nil {
		t.Fatalf("blockers: %v", err)
	}
	if !reflect.DeepEqual(blockers, []string{"ENG-1"}) {
		t.Errorf("blockers of ACME-1 = %v, want the internal issue's edge preserved", blockers)
	}
}
