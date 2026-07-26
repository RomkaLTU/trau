package pipeline

import (
	"context"
	"slices"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

// forkGit is a tiny commit graph: head is the commit the branch sits on, contains
// lists per commit what is reachable from it (itself included), and mergeBase is what
// the run's base branch shares with the branch.
type forkGit struct {
	fakeGit
	head      string
	mergeBase string
	contains  map[string][]string
}

func (g *forkGit) HeadSHA(context.Context) (string, error) { return g.head, nil }

func (g *forkGit) MergeBase(context.Context, string, string) (string, error) {
	return g.mergeBase, nil
}

func (g *forkGit) IsAncestor(_ context.Context, ancestor, rev string) (bool, error) {
	if rev == "HEAD" {
		rev = g.head
	}
	return slices.Contains(g.contains[rev], ancestor), nil
}

func (g *forkGit) ResolvesToCommit(_ context.Context, rev string) (bool, error) {
	_, ok := g.contains[rev]
	return ok, nil
}

// rewiringRunner stands in for a build agent that moves the branch onto another
// line of history while it works, taking the commits in gone off the graph the way
// a discarded rebase leaves the commit it started from unreachable.
type rewiringRunner struct {
	git       *forkGit
	head      string
	mergeBase string
	gone      []string
}

func (r rewiringRunner) Run(context.Context, string, string) (agent.Result, error) {
	r.git.head, r.git.mergeBase = r.head, r.mergeBase
	for _, sha := range r.gone {
		delete(r.git.contains, sha)
	}
	return agent.Result{}, nil
}

func TestBuildPinsTheForkPoint(t *testing.T) {
	t.Run("a fresh branch pins the commit it was cut at", func(t *testing.T) {
		id := "COD-1"
		g := &forkGit{
			head:      "cut-here",
			mergeBase: "cut-here",
			contains:  map[string][]string{"cut-here": {"cut-here"}},
		}
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Git = g

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := p.State.Get(id, "BASE_SHA"); got != "cut-here" {
			t.Errorf("BASE_SHA = %q, want the commit the branch was cut at", got)
		}
	})

	t.Run("a branch that still contains the pin keeps it", func(t *testing.T) {
		id := "COD-2"
		g := &forkGit{
			head:      "cut-here",
			mergeBase: "cut-here",
			contains: map[string][]string{
				"cut-here":   {"cut-here"},
				"run-commit": {"cut-here", "run-commit"},
			},
		}
		p := newTestPipeline(t, rewiringRunner{git: g, head: "run-commit", mergeBase: "cut-here"}, &fakeTracker{})
		p.Git = g

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := p.State.Get(id, "BASE_SHA"); got != "cut-here" {
			t.Errorf("BASE_SHA = %q, want the original pin — the run's own commits must stay in its diff", got)
		}
	})

	t.Run("a build that rebases onto an advanced base re-anchors at the new fork point", func(t *testing.T) {
		id := "COD-3"
		g := &forkGit{
			head:      "cut-here",
			mergeBase: "cut-here",
			contains: map[string][]string{
				"cut-here":    {"cut-here"},
				"base-moved":  {"cut-here", "base-moved"},
				"replayed-on": {"cut-here", "base-moved", "replayed-on"},
			},
		}
		p := newTestPipeline(t, rewiringRunner{git: g, head: "replayed-on", mergeBase: "base-moved"}, &fakeTracker{})
		p.Git = g

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := p.State.Get(id, "BASE_SHA"); got != "base-moved" {
			t.Errorf("BASE_SHA = %q, want the advanced base the branch was replayed onto", got)
		}
	})

	t.Run("a build that rewires the branch re-anchors below its replayed commits", func(t *testing.T) {
		id := "COD-4"
		g := &forkGit{
			head:      "cut-here",
			mergeBase: "cut-here",
			contains: map[string][]string{
				"cut-here":   {"cut-here"},
				"other-line": {"other-line"},
				"replayed":   {"other-line", "replayed"},
			},
		}
		p := newTestPipeline(t, rewiringRunner{git: g, head: "replayed", mergeBase: "other-line"}, &fakeTracker{})
		p.Git = g

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := p.State.Get(id, "BASE_SHA"); got != "other-line" {
			t.Errorf("BASE_SHA = %q, want the fork point the branch was rewired onto, not its head", got)
		}
	})

	t.Run("a pin whose commit is gone re-anchors at the merge base", func(t *testing.T) {
		id := "COD-5"
		g := &forkGit{
			head:      "cut-here",
			mergeBase: "cut-here",
			contains: map[string][]string{
				"cut-here":   {"cut-here"},
				"other-line": {"other-line"},
				"replayed":   {"other-line", "replayed"},
			},
		}
		p := newTestPipeline(t, rewiringRunner{git: g, head: "replayed", mergeBase: "other-line", gone: []string{"cut-here"}}, &fakeTracker{})
		p.Git = g

		if err := p.Build(context.Background(), id); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := p.State.Get(id, "BASE_SHA"); got != "other-line" {
			t.Errorf("BASE_SHA = %q, want the merge base — a pin git can no longer resolve anchors nothing", got)
		}
	})
}
