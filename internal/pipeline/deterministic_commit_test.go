package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// commitGit records the staging + commit a deterministic commit makes, and can be
// primed to fail either step so the fault-classification path can be exercised.
type commitGit struct {
	fakeGit
	addAllErr   error
	commitErr   error
	addAllCalls int
	committed   bool
	message     string
	noVerify    bool
}

func (g *commitGit) AddAll(context.Context) error {
	g.addAllCalls++
	return g.addAllErr
}

func (g *commitGit) Commit(_ context.Context, msg string, noVerify bool) error {
	if g.commitErr != nil {
		return g.commitErr
	}
	g.committed = true
	g.message = msg
	g.noVerify = noVerify
	return nil
}

func TestDeterministicCommitMessage(t *testing.T) {
	long := "Add a genuinely long ticket title that comfortably exceeds the seventy-two character subject budget"
	cases := []struct {
		name        string
		id          string
		title       string
		wantType    string
		wantSubject string
	}{
		{"feature default", "COD-1", "Deterministic commit phase for squash-merge repos", "feat", "Deterministic commit phase for squash-merge repos"},
		{"fix from leading verb", "COD-2", "Fix drain report parent dir creation", "fix", "Fix drain report parent dir creation"},
		{"bug word", "COD-3", "Bug: merged ticket re-pick fault", "fix", "Bug: merged ticket re-pick fault"},
		{"refactor", "COD-4", "Refactor the router dispatch table", "refactor", "Refactor the router dispatch table"},
		{"docs", "COD-5", "Document the config precedence order", "docs", "Document the config precedence order"},
		{"test", "COD-6", "Test the epic finalize sync path", "test", "Test the epic finalize sync path"},
		{"chore", "COD-7", "Chore: bump the goreleaser action", "chore", "Chore: bump the goreleaser action"},
		{"empty title falls back to id", "COD-8", "", "feat", "COD-8"},
		{"whitespace title falls back to id", "COD-9", "   ", "feat", "COD-9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := deterministicCommitMessage(c.id, c.title)
			wantHeader := c.wantType + ": " + c.wantSubject
			header, _, _ := strings.Cut(msg, "\n")
			if header != wantHeader {
				t.Errorf("header = %q, want %q", header, wantHeader)
			}
			if !strings.HasSuffix(msg, "\n\nRefs: "+c.id) {
				t.Errorf("message %q missing 'Refs: %s' trailer separated by a blank line", msg, c.id)
			}
			for _, banned := range []string{"Co-authored-by", "Co-Authored-By", "Generated with Claude"} {
				if strings.Contains(msg, banned) {
					t.Errorf("message must never contain %q: %q", banned, msg)
				}
			}
		})
	}

	t.Run("subject truncated to 72 at a word boundary", func(t *testing.T) {
		msg := deterministicCommitMessage("COD-10", long)
		subject, _, _ := strings.Cut(msg, "\n")
		subject = strings.TrimPrefix(subject, "feat: ")
		if len([]rune(subject)) > 72 {
			t.Errorf("subject %q is %d runes, want ≤72", subject, len([]rune(subject)))
		}
		if !strings.HasPrefix(long, subject) {
			t.Errorf("subject %q is not a prefix of the title", subject)
		}
		if strings.HasSuffix(subject, " ") || strings.Contains(subject, "  ") {
			t.Errorf("subject %q has ragged whitespace", subject)
		}
	})
}

// TestDeterministicCommitStagesAndCommits: the deterministic path stages everything
// (git add -A) and commits with hooks live (noVerify=false), pulling the subject
// from the checkpointed TITLE and never spawning the commit agent.
func TestDeterministicCommitStagesAndCommits(t *testing.T) {
	git := &commitGit{}
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Git = git
	p.MergeMethod = "squash"
	p.DeterministicCommit = true
	if err := p.State.Set("COD-800", "TITLE", "Deterministic commit phase for squash-merge repos"); err != nil {
		t.Fatal(err)
	}

	if err := p.commitSlice(context.Background(), "COD-800"); err != nil {
		t.Fatalf("commitSlice = %v, want nil", err)
	}
	if git.addAllCalls != 1 {
		t.Errorf("AddAll calls = %d, want 1", git.addAllCalls)
	}
	if !git.committed {
		t.Fatal("Commit was not called")
	}
	if git.noVerify {
		t.Error("deterministic commit must run hooks (noVerify=false) so a pre-commit rejection faults normally")
	}
	if want := "feat: Deterministic commit phase for squash-merge repos"; !strings.HasPrefix(git.message, want) {
		t.Errorf("commit message = %q, want it to start with %q", git.message, want)
	}
	if !strings.HasSuffix(git.message, "Refs: COD-800") {
		t.Errorf("commit message = %q, want a 'Refs: COD-800' trailer", git.message)
	}
	if runner.calls != 0 {
		t.Errorf("commit agent calls = %d, want 0 (deterministic path spawns no agent)", runner.calls)
	}
}

// TestDeterministicCommitPropagatesFailures: a failing stage or commit (e.g. a
// pre-commit hook rejection) surfaces as a plain error so classifyPhaseErr routes it
// through the fault path with the WIP preserved.
func TestDeterministicCommitPropagatesFailures(t *testing.T) {
	t.Run("stage failure", func(t *testing.T) {
		git := &commitGit{addAllErr: errors.New("disk full")}
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Git = git
		p.MergeMethod = "squash"
		p.DeterministicCommit = true

		err := p.commitSlice(context.Background(), "COD-800")
		if err == nil || IsPaused(err) || isGiveUp(err) {
			t.Fatalf("commitSlice = %v, want a plain error (funnels to the fault path)", err)
		}
	})
	t.Run("commit failure", func(t *testing.T) {
		git := &commitGit{commitErr: errors.New("pre-commit hook rejected the change")}
		p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
		p.Git = git
		p.MergeMethod = "squash"
		p.DeterministicCommit = true

		err := p.commitSlice(context.Background(), "COD-800")
		if err == nil || IsPaused(err) || isGiveUp(err) {
			t.Fatalf("commitSlice = %v, want a plain error (funnels to the fault path)", err)
		}
	})
}

// TestCommitSliceRouting: only a squash repo with DeterministicCommit on skips the
// commit agent. The opt-out and every non-squash method keep the agent commit.
func TestCommitSliceRouting(t *testing.T) {
	cases := []struct {
		name          string
		mergeMethod   string
		deterministic bool
		wantAgent     bool
	}{
		{"squash deterministic skips agent", "squash", true, false},
		{"squash opt-out keeps agent", "squash", false, true},
		{"merge keeps agent", "merge", true, true},
		{"rebase keeps agent", "rebase", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git := &commitGit{}
			runner := &countingRunner{results: []error{nil}, name: "claude"}
			p := newTestPipeline(t, runner, &fakeTracker{})
			p.Git = git
			p.MergeMethod = c.mergeMethod
			p.DeterministicCommit = c.deterministic
			if err := p.State.Set("COD-800", "TITLE", "Some slice title"); err != nil {
				t.Fatal(err)
			}

			if err := p.commitSlice(context.Background(), "COD-800"); err != nil {
				t.Fatalf("commitSlice = %v, want nil", err)
			}
			gotAgent := runner.calls > 0
			if gotAgent != c.wantAgent {
				t.Errorf("agent used = %v (calls=%d), want %v", gotAgent, runner.calls, c.wantAgent)
			}
			if c.wantAgent && git.committed {
				t.Error("agent path must not run the deterministic git commit")
			}
			if !c.wantAgent && !git.committed {
				t.Error("deterministic path must commit via git")
			}
		})
	}
}
