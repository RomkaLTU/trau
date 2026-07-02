package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/state"
)

// TestClassifyRemotePushErr guards the hook-manager-agnostic classifier: it must
// read only git's own ref-level markers, never hook-tool output. A nil probe (the
// remote would accept the ref with hooks bypassed) means a local hook was the sole
// blocker.
func TestClassifyRemotePushErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want pushOutcome
	}{
		{"remote would accept (local hook only)", nil, pushLocalHook},
		{"non-fast-forward", errors.New("git push: exit status 1: ! [rejected] main -> main (non-fast-forward)\nhint: Updates were rejected; fetch first"), pushNonFastForward},
		{"remote-side hook decline", errors.New("git push: exit status 1: ! [remote rejected] main -> main (pre-receive hook declined)"), pushRemoteRejected},
		{"network hiccup is transient", errors.New("git push: exit status 128: fatal: unable to access: Could not resolve host"), pushTransient},
		{"auth failure is deterministic", errors.New("git push: exit status 128: fatal: Authentication failed"), pushDeterministic},
	}
	for _, c := range cases {
		if got := classifyRemotePushErr(c.err); got != c.want {
			t.Errorf("%s: classifyRemotePushErr = %d, want %d", c.name, got, c.want)
		}
	}
}

// hookGit simulates a repo whose pre-push hook rejects the diff: real (hooks-live)
// pushes return the queued errors, while the hook-bypassing dry-run probe returns
// dryRunErr (nil = the remote itself would accept the ref → local hook only).
type hookGit struct {
	fakeGit
	branch      string
	pushErrs    []error
	pushCalls   int
	pushNoVerif int
	dryRunErr   error
	dryRunCalls int
}

func (g *hookGit) CurrentBranch(context.Context) (string, error) { return g.branch, nil }

func (g *hookGit) Push(_ context.Context, _, _ string, noVerify bool) error {
	if noVerify {
		g.pushNoVerif++
		return nil
	}
	i := g.pushCalls
	g.pushCalls++
	if i < len(g.pushErrs) {
		return g.pushErrs[i]
	}
	return nil
}

func (g *hookGit) PushDryRun(context.Context, string, string) error {
	g.dryRunCalls++
	return g.dryRunErr
}

// TestPushDeliverableRepairsLocalHookThenSucceeds is the COD-659 core: a local
// pre-push hook rejection routes into the bounded repair loop (one agent pass,
// one hook run per attempt — no blind 3× retry), and a green re-push proceeds.
func TestPushDeliverableRepairsLocalHookThenSucceeds(t *testing.T) {
	hookErr := errors.New("git push: exit status 1: PHPStan OK, Pest failed 1 test")
	git := &hookGit{branch: "feature/COD-659-x", pushErrs: []error{hookErr}}
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Git = git
	p.Remote = "origin"
	p.MaxRepairs = 2

	if err := p.pushDeliverable(context.Background(), "COD-659", "HEAD"); err != nil {
		t.Fatalf("pushDeliverable = %v, want nil after a successful repair", err)
	}
	if runner.calls != 1 {
		t.Errorf("repair agent calls = %d, want 1", runner.calls)
	}
	if git.pushCalls != 2 {
		t.Errorf("hooks-live pushes = %d, want 2 (one rejected, one green — no blind retry)", git.pushCalls)
	}
}

// TestPushDeliverableExhaustsRepairsThenGivesUp: a persistent hook rejection is
// never faulted — repairs exhaust into a normal give-up (quarantine, WIP preserved),
// so the session moves on to the next ticket.
func TestPushDeliverableExhaustsRepairsThenGivesUp(t *testing.T) {
	hookErr := errors.New("git push: exit status 1: lint failed")
	git := &hookGit{branch: "feature/COD-659-y", pushErrs: []error{hookErr, hookErr, hookErr}}
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	tr := &fakeTracker{}
	p := newTestPipeline(t, runner, tr)
	p.Git = git
	p.Remote = "origin"
	p.MaxRepairs = 1

	err := p.pushDeliverable(context.Background(), "COD-659", "HEAD")

	var g *GiveUpError
	if !errors.As(err, &g) {
		t.Fatalf("pushDeliverable = %v, want a *GiveUpError (give up, not fault)", err)
	}
	if IsFault(err) {
		t.Errorf("a hook rejection must NOT fault the session: %v", err)
	}
	if tr.quarantineCalls != 1 {
		t.Errorf("Quarantine calls = %d, want 1", tr.quarantineCalls)
	}
	if got := p.State.Get("COD-659", "PHASE"); got != state.Quarantined {
		t.Errorf("PHASE = %q, want quarantined", got)
	}
	if runner.calls != 1 {
		t.Errorf("repair agent calls = %d, want 1 (MaxRepairs)", runner.calls)
	}
	if git.pushNoVerif == 0 {
		t.Error("give-up WIP-preservation push must bypass hooks (--no-verify)")
	}
}

// TestPushDeliverableRetriesTransient proves the auth/network path is untouched: a
// transient failure still retries with backoff (no repair agent) and then succeeds.
func TestPushDeliverableRetriesTransient(t *testing.T) {
	netErr := errors.New("git push: exit status 128: fatal: unable to access: Could not resolve host")
	git := &hookGit{branch: "feature/COD-659-z", pushErrs: []error{netErr, netErr}, dryRunErr: netErr}
	runner := &countingRunner{results: []error{nil}, name: "claude"}
	p := newTestPipeline(t, runner, &fakeTracker{})
	p.Git = git
	p.Remote = "origin"
	p.MaxRepairs = 2
	p.Sleep = func(time.Duration) {}

	if err := p.pushDeliverable(context.Background(), "COD-659", "HEAD"); err != nil {
		t.Fatalf("pushDeliverable = %v, want nil after transient retries", err)
	}
	if runner.calls != 0 {
		t.Errorf("repair agent calls = %d, want 0 (transient is not repairable)", runner.calls)
	}
	if git.pushCalls != 3 {
		t.Errorf("pushes = %d, want 3 (two transient + one green)", git.pushCalls)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeHook(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'pre-push gate: 1 test failed' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestExecGitPushHookRejection is the acceptance-criteria integration test against
// real git: a rejecting pre-push hook (both a plain .git/hooks script and a
// core.hooksPath/husky-style setup) fails a hooks-live push, while the dry-run probe
// succeeds (the differential signal that a LOCAL hook is the only blocker) and a
// --no-verify push goes through.
func TestExecGitPushHookRejection(t *testing.T) {
	for _, mode := range []string{"dot-git-hooks", "core-hooks-path"} {
		t.Run(mode, func(t *testing.T) {
			remote := t.TempDir()
			gitRun(t, remote, "init", "--bare")
			work := t.TempDir()
			gitRun(t, work, "init")
			if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRun(t, work, "add", "-A")
			gitRun(t, work, "commit", "-m", "init")
			gitRun(t, work, "remote", "add", "origin", remote)

			switch mode {
			case "dot-git-hooks":
				writeHook(t, filepath.Join(work, ".git", "hooks", "pre-push"))
			case "core-hooks-path":
				hooks := filepath.Join(work, ".husky")
				gitRun(t, work, "config", "core.hooksPath", hooks)
				writeHook(t, filepath.Join(hooks, "pre-push"))
			}

			g := ExecGit{Repo: work}
			ctx := context.Background()

			if err := g.Push(ctx, "origin", "HEAD", false); err == nil {
				t.Fatal("hooks-live push should be rejected by the pre-push hook")
			}
			if err := g.PushDryRun(ctx, "origin", "HEAD"); err != nil {
				t.Fatalf("dry-run probe (hooks bypassed) should succeed against a reachable remote: %v", err)
			}
			if got := classifyRemotePushErr(g.PushDryRun(ctx, "origin", "HEAD")); got != pushLocalHook {
				t.Errorf("classify = %d, want pushLocalHook (%d)", got, pushLocalHook)
			}
			if err := g.Push(ctx, "origin", "HEAD", true); err != nil {
				t.Fatalf("--no-verify push should bypass the hook: %v", err)
			}
		})
	}
}
