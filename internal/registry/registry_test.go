package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// deadPID starts a child, kills and reaps it, and returns a PID that is now
// guaranteed not to name a running process.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return pid
}

func TestRegisterAppearsLiveThenDeregisters(t *testing.T) {
	home := t.TempDir()

	h := Register(home, "/repo/acme", "/repo/acme/.trau/runs")
	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live after Register = %d entries, want 1", len(live))
	}
	if live[0].PID != os.Getpid() {
		t.Errorf("entry PID = %d, want own %d", live[0].PID, os.Getpid())
	}
	if live[0].RepoRoot != "/repo/acme" {
		t.Errorf("RepoRoot = %q, want /repo/acme", live[0].RepoRoot)
	}

	h.Deregister()
	if live := Live(home); len(live) != 0 {
		t.Fatalf("Live after Deregister = %d entries, want 0", len(live))
	}
}

func TestLiveReapsDeadEntries(t *testing.T) {
	home := t.TempDir()
	Register(home, "/repo/live", ".trau/runs")

	dead := Entry{PID: deadPID(t), RepoRoot: "/repo/dead", RunsDir: "/repo/dead/.trau/runs"}
	deadFile := filepath.Join(instancesDir(home), entryName(dead.PID))
	if err := writeJSON(deadFile, dead); err != nil {
		t.Fatalf("write dead entry: %v", err)
	}

	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1 (the dead one reaped)", len(live))
	}
	if live[0].PID != os.Getpid() {
		t.Errorf("surviving PID = %d, want own %d", live[0].PID, os.Getpid())
	}
	if _, err := os.Stat(deadFile); !os.IsNotExist(err) {
		t.Errorf("dead entry file still present, want reaped")
	}
}

func TestRegisterIsBestEffortWhenUnwritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	home := filepath.Join(blocker, "trau") // parent is a file: every write fails

	h := Register(home, "/repo/acme", ".trau/runs")
	if live := Live(home); len(live) != 0 {
		t.Fatalf("Live over unwritable home = %d, want 0", len(live))
	}
	h.Deregister() // must not panic
}

func TestRelativeRunsDirResolvedAgainstRepoRoot(t *testing.T) {
	home := t.TempDir()
	Register(home, "/repo/acme", ".trau/runs")

	live := Live(home)
	if len(live) != 1 {
		t.Fatalf("Live = %d entries, want 1", len(live))
	}
	if want := "/repo/acme/.trau/runs"; live[0].RunsDir != want {
		t.Errorf("RunsDir = %q, want %q", live[0].RunsDir, want)
	}
}

func TestReposOutliveTheLoop(t *testing.T) {
	home := t.TempDir()
	h := Register(home, "/repo/acme", "/repo/acme/.trau/runs")

	RememberRepos(home, Live(home))
	h.Deregister()

	if live := Live(home); len(live) != 0 {
		t.Fatalf("Live after Deregister = %d, want 0", len(live))
	}
	repos := Repos(home)
	if len(repos) != 1 {
		t.Fatalf("Repos after loop exit = %d, want 1", len(repos))
	}
	if repos[0].Name != "acme" || repos[0].Root != "/repo/acme" {
		t.Errorf("repo = %+v, want name acme root /repo/acme", repos[0])
	}
}
