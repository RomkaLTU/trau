package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
)

// stubGitLogEnv points a re-executed test binary at the file it appends one line
// per mutating invocation to; its presence is what makes the binary stand in for
// git. stubGitFailEnv scripts those invocations' outcomes, "|"-separated, one per
// call — calls past the end of the list succeed.
const (
	stubGitLogEnv  = "TRAU_TEST_STUB_GIT_LOG"
	stubGitFailEnv = "TRAU_TEST_STUB_GIT_FAIL"
)

// stubGitMain answers as `git -C <repo> ...` so a test can count the attempts run
// makes and choose each one's failure. Re-executing the test binary is the only
// stand-in binary that also runs on native Windows.
func stubGitMain(log string, args []string) int {
	repo := args[1]
	if slices.Contains(args, "rev-parse") {
		fmt.Println(filepath.Join(repo, ".git"))
		return 0
	}

	n := len(stubGitCalls(log))
	f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(f, strings.Join(args[2:], " "))
	_ = f.Close()

	fails := strings.Split(os.Getenv(stubGitFailEnv), "|")
	if n >= len(fails) || fails[n] == "" {
		return 0
	}
	fmt.Println(fails[n])
	return 1
}

func stubGitCalls(log string) []string {
	data, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	var calls []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

// TestExecGitRunStaleIndexLock pins the whole recovery contract: an index.lock left
// behind by a dead git is removed, the removal logged at operator level, and the
// command retried exactly once; a lock a live git could still own — and any
// unrelated failure — leaves the lock alone, skips the retry, and returns the
// original error unmodified.
func TestExecGitRunStaleIndexLock(t *testing.T) {
	const (
		collision = "fatal: Unable to create '.git/index.lock': File exists."
		relocked  = "fatal: Unable to create '.git/index.lock': File exists, once more."
		unrelated = "error: pathspec 'nope' did not match any file(s) known to git"
	)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		lock      bool
		body      string
		age       time.Duration
		fail      string
		wantErr   bool
		wantCalls int
		wantLock  bool
		wantLog   bool
	}{
		{
			name: "a stale lock is removed, logged and the command retried once",
			lock: true, age: 5 * time.Minute, fail: collision,
			wantCalls: 2, wantLog: true,
		},
		{
			name: "a young lock still belongs to a live git",
			lock: true, age: time.Second, fail: collision,
			wantErr: true, wantCalls: 1, wantLock: true,
		},
		{
			name: "a non-empty lock still belongs to a live git",
			lock: true, body: "37142", age: 5 * time.Minute, fail: collision,
			wantErr: true, wantCalls: 1, wantLock: true,
		},
		{
			name:    "a collision with no lock left to clear is not retried",
			fail:    collision,
			wantErr: true, wantCalls: 1,
		},
		{
			name: "an unrelated failure is not retried",
			lock: true, age: 5 * time.Minute, fail: unrelated,
			wantErr: true, wantCalls: 1, wantLock: true,
		},
		{
			name: "a second collision returns the original error",
			lock: true, age: 5 * time.Minute, fail: collision + "|" + relocked,
			wantErr: true, wantCalls: 2, wantLog: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(repo, ".git", "index.lock")
			if tc.lock {
				writeStaleLock(t, lock, tc.body, tc.age)
			}

			callLog := filepath.Join(t.TempDir(), "calls")
			t.Setenv(stubGitLogEnv, callLog)
			t.Setenv(stubGitFailEnv, tc.fail)

			var log bytes.Buffer
			logger.Init(&log, false, false)
			t.Cleanup(func() { logger.Init(os.Stderr, false, false) })

			err := ExecGit{Bin: self, Repo: repo}.AddAll(context.Background())

			fails := strings.Split(tc.fail, "|")
			switch {
			case !tc.wantErr && err != nil:
				t.Fatalf("AddAll = %v, want nil", err)
			case tc.wantErr && (err == nil || !strings.Contains(err.Error(), fails[0])):
				t.Fatalf("AddAll = %v, want the first failure %q", err, fails[0])
			}
			for _, later := range fails[1:] {
				if err != nil && strings.Contains(err.Error(), later) {
					t.Errorf("AddAll reported the retry's failure, want the original untouched")
				}
			}

			if calls := stubGitCalls(callLog); len(calls) != tc.wantCalls {
				t.Errorf("git ran %d time(s) %v, want %d", len(calls), calls, tc.wantCalls)
			}
			if _, err := os.Stat(lock); (err == nil) != tc.wantLock {
				t.Errorf("index.lock present = %v, want %v", err == nil, tc.wantLock)
			}
			logged := strings.Contains(log.String(), "index.lock") && strings.Contains(log.String(), repo)
			if logged != tc.wantLog {
				t.Errorf("removal logged at operator level = %v, want %v (log %q)", logged, tc.wantLog, log.String())
			}
		})
	}
}

// TestExecGitRunStaleIndexLockInWorktree runs the recovery against real git and
// pins the git-dir resolution: a linked worktree keeps its index under
// .git/worktrees/<name>, so assuming <repo>/.git would leave the orphan in place.
func TestExecGitRunStaleIndexLockInWorktree(t *testing.T) {
	main := t.TempDir()
	gitRun(t, main, "init")
	write(t, filepath.Join(main, "keep.txt"), "a\n")
	gitRun(t, main, "add", "-A")
	gitRun(t, main, "commit", "-m", "init")

	linked := filepath.Join(t.TempDir(), "wt")
	gitRun(t, main, "worktree", "add", "-b", "scratch", linked)

	write(t, filepath.Join(linked, "fresh.txt"), "x\n")
	lock := filepath.Join(main, ".git", "worktrees", "wt", "index.lock")
	writeStaleLock(t, lock, "", 5*time.Minute)

	if err := (ExecGit{Repo: linked}).AddAll(context.Background()); err != nil {
		t.Fatalf("AddAll = %v, want nil", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("index.lock still present after recovery")
	}
}

func writeStaleLock(t *testing.T, path, body string, age time.Duration) {
	t.Helper()
	write(t, path, body)
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}
