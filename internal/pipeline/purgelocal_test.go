package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/state"
)

// purgeGit records the checkout and branch drops a local purge performs, answers
// FindFeatureBranch for a ticket whose checkpoint recorded no branch, and stands in
// for a remote that still holds the branch, pruned it already, or cannot be reached.
type purgeGit struct {
	fakeGit
	found       string
	remoteHas   bool
	remoteErr   error
	checkedOut  string
	deleted     string
	deletedPush string
}

func (g *purgeGit) FindFeatureBranch(context.Context, string) (string, error) {
	return g.found, nil
}

func (g *purgeGit) Checkout(_ context.Context, branch string, _ bool) error {
	g.checkedOut = branch
	return nil
}

func (g *purgeGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = branch
	return nil
}

func (g *purgeGit) RemoteBranchExists(context.Context, string, string) (bool, error) {
	return g.remoteHas, g.remoteErr
}

func (g *purgeGit) DeletePushedBranch(_ context.Context, remote, branch string) error {
	g.deletedPush = remote + "/" + branch
	return nil
}

// keptCheckpoints records the checkpoint drops a purge makes. With the hub-backed
// store that call is what takes a run off the ledger, so a purge must make none.
type keptCheckpoints struct {
	state.Checkpoints
	removed []string
}

func (c *keptCheckpoints) RemoveState(id string) error {
	c.removed = append(c.removed, id)
	return c.Checkpoints.RemoveState(id)
}

// TestPurgeLocalDropsTheGitFootprintWithoutTheTracker pins what a hard-delete
// spawns: the repo goes back to base, both branch refs go, the ticket's run
// directory goes with them, and the tombstoned ticket's upstream issue is never
// touched.
func TestPurgeLocalDropsTheGitFootprintWithoutTheTracker(t *testing.T) {
	id := "COD-1094"
	branch := "feature/COD-1094-hard-delete"
	tr := &resetTracker{}
	p := newTestPipeline(t, fakeRunner{}, tr)
	g := &purgeGit{remoteHas: true}
	p.Git = g
	p.Remote = "origin"
	if err := p.State.Set(id, "BRANCH", branch); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	runDir := filepath.Join(p.RunsDir, id)
	if err := os.WriteFile(filepath.Join(runDir, "build.log"), []byte("phase log\n"), 0o644); err != nil {
		t.Fatalf("seed phase log: %v", err)
	}

	if err := p.PurgeLocal(context.Background(), id); err != nil {
		t.Fatalf("PurgeLocal: %v", err)
	}

	if g.checkedOut != "main" {
		t.Errorf("checked out %q, want the base branch main", g.checkedOut)
	}
	if g.deleted != branch {
		t.Errorf("deleted local branch %q, want %q", g.deleted, branch)
	}
	if want := "origin/" + branch; g.deletedPush != want {
		t.Errorf("deleted pushed branch %q, want %q", g.deletedPush, want)
	}
	if _, err := os.Stat(runDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("run dir stat = %v, want %s removed", err, runDir)
	}
	if tr.resetCalls != 0 {
		t.Errorf("tracker Reset calls = %d, want 0 — a hard-deleted ticket's upstream issue is not ours to touch", tr.resetCalls)
	}
}

// TestPurgeLocalKeepsTheRunHistory is the COD-1094 regression guard: a purge takes
// the git footprint down, never the hub's record of what ran. Issues.Purge leaves
// run data standing, and the cleanup it spawns must too, or hard-deleting a ticket
// silently erases its run from the ledger.
func TestPurgeLocalKeepsTheRunHistory(t *testing.T) {
	id := "COD-1094"
	p := newTestPipeline(t, fakeRunner{}, &resetTracker{})
	p.Git = &purgeGit{}
	checkpoints := &keptCheckpoints{Checkpoints: p.State}
	p.State = checkpoints
	logs := p.PhaseLogs.(*memPhaseLogs)
	artifacts := newMemArtifacts()
	p.Artifacts = artifacts
	if err := logs.Put(id, "build", "phase log"); err != nil {
		t.Fatalf("seed phase log: %v", err)
	}
	if err := artifacts.Put(id, artifactHandoff, "handoff"); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	if err := p.PurgeLocal(context.Background(), id); err != nil {
		t.Fatalf("PurgeLocal: %v", err)
	}

	if len(checkpoints.removed) != 0 {
		t.Errorf("dropped checkpoints %v, want none — the run stays on the ledger", checkpoints.removed)
	}
	if _, ok := logs.get(id, "build"); !ok {
		t.Error("phase log dropped, want it browsable after the ticket is gone")
	}
	if _, ok, err := artifacts.Get(id, artifactHandoff); err != nil || !ok {
		t.Errorf("handoff artifact: ok=%v err=%v, want it browsable after the ticket is gone", ok, err)
	}
}

// TestPurgeLocalFallsBackToTheDiscoveredBranch covers a ticket whose checkpoint
// never recorded a BRANCH: the branch trau cut is still found and dropped.
func TestPurgeLocalFallsBackToTheDiscoveredBranch(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &resetTracker{})
	g := &purgeGit{found: "feature/COD-1095-orphan", remoteHas: true}
	p.Git = g
	p.Remote = "origin"

	if err := p.PurgeLocal(context.Background(), "COD-1095"); err != nil {
		t.Fatalf("PurgeLocal: %v", err)
	}

	if g.deleted != g.found {
		t.Errorf("deleted local branch %q, want the discovered %q", g.deleted, g.found)
	}
	if want := "origin/" + g.found; g.deletedPush != want {
		t.Errorf("deleted pushed branch %q, want %q", g.deletedPush, want)
	}
}

// TestPurgeLocalReportsAnUnreachableRemote: a remote that cannot be consulted
// leaves the pushed branch behind, so the child must say so — that error is the
// only way the failure reaches the hub log. The local footprint still goes.
func TestPurgeLocalReportsAnUnreachableRemote(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &resetTracker{})
	g := &purgeGit{found: "feature/COD-1096-dead-remote", remoteErr: errors.New("ls-remote origin: signal: killed")}
	p.Git = g
	p.Remote = "origin"

	err := p.PurgeLocal(context.Background(), "COD-1096")

	if err == nil {
		t.Fatal("PurgeLocal err = nil, want the unreachable remote reported so the hub logs it")
	}
	if g.deleted != g.found {
		t.Errorf("deleted local branch %q, want %q dropped before the remote is consulted", g.deleted, g.found)
	}
}

// TestPurgeLocalIgnoresAPrunedRemoteBranch: a ticket that never pushed has nothing
// on the remote, and that is not a cleanup failure worth logging.
func TestPurgeLocalIgnoresAPrunedRemoteBranch(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &resetTracker{})
	g := &purgeGit{found: "feature/COD-1097-never-pushed"}
	p.Git = g
	p.Remote = "origin"

	if err := p.PurgeLocal(context.Background(), "COD-1097"); err != nil {
		t.Fatalf("PurgeLocal: %v", err)
	}

	if g.deletedPush != "" {
		t.Errorf("deleted pushed branch %q, want no remote delete attempted", g.deletedPush)
	}
}
