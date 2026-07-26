package teamsync

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterID(t *testing.T) {
	tests := []struct {
		name          string
		email, other  string
		machine, mach string
		wantSame      bool
	}{
		{name: "same identity and machine is stable", email: "dev@example.com", other: "dev@example.com", machine: "laptop", mach: "laptop", wantSame: true},
		{name: "case and padding are normalized", email: "Dev@Example.com", other: " dev@example.com ", machine: "laptop", mach: "laptop", wantSame: true},
		{name: "one person on two machines differs", email: "dev@example.com", other: "dev@example.com", machine: "laptop", mach: "desktop"},
		{name: "two people on one machine differ", email: "dev@example.com", other: "other@example.com", machine: "laptop", mach: "laptop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, want := WriterID(tt.email, tt.machine), WriterID(tt.other, tt.mach)
			if same := got == want; same != tt.wantSame {
				t.Errorf("WriterID(%q,%q) == WriterID(%q,%q) = %v, want %v", tt.email, tt.machine, tt.other, tt.mach, same, tt.wantSame)
			}
		})
	}
}

func TestWriterIDIsRefSafe(t *testing.T) {
	id := WriterID("dev@example.com", "laptop.local")
	if len(id) != writerIDLen {
		t.Errorf("len = %d, want %d", len(id), writerIDLen)
	}
	if strings.Trim(id, "0123456789abcdef") != "" {
		t.Errorf("WriterID = %q, want lowercase hex", id)
	}
}

func TestPushSnapshotsAndForcePushes(t *testing.T) {
	remote, repo := fixture(t)
	cfg := Config{RepoDir: repo}
	w := Writer{ID: "abc123", Name: "Ada", Email: "ada@example.com"}

	if err := Push(context.Background(), cfg, w, Snapshot{Lessons: []Lesson{{Lesson: "first"}}}); err != nil {
		t.Fatalf("first push: %v", err)
	}
	first := remoteRef(t, remote, RefPrefix+w.ID)

	if err := Push(context.Background(), cfg, w, Snapshot{Lessons: []Lesson{{Lesson: "second"}}}); err != nil {
		t.Fatalf("second push: %v", err)
	}
	second := remoteRef(t, remote, RefPrefix+w.ID)

	if first == second {
		t.Fatal("second push did not move the ref")
	}
	if parents := git(t, remote, "rev-list", "--parents", "-n", "1", second); len(strings.Fields(parents)) != 1 {
		t.Errorf("snapshot commit has parents: %q", parents)
	}
	if got := git(t, remote, "cat-file", "blob", second+":"+payloadFile); !strings.Contains(got, "second") {
		t.Errorf("payload = %q, want the latest lessons", got)
	}
}

func TestPushRejectedByRemoteReturnsError(t *testing.T) {
	_, repo := fixture(t)
	git(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	err := Push(context.Background(), Config{RepoDir: repo}, Writer{ID: "abc123"}, Snapshot{})
	if err == nil {
		t.Fatal("push to an unreachable remote succeeded")
	}
}

func TestFetchReadsTeammatePayloadsAndSkipsSelf(t *testing.T) {
	remote, mine := fixture(t)
	theirs := clone(t, remote, "theirs")

	me := Writer{ID: "mine00", Name: "Ada", Email: "ada@example.com"}
	them := Writer{ID: "theirs0", Name: "Grace", Email: "grace@example.com"}
	if err := Push(context.Background(), Config{RepoDir: mine}, me, Snapshot{Lessons: []Lesson{{Lesson: "mine"}}}); err != nil {
		t.Fatalf("publish mine: %v", err)
	}
	if err := Push(context.Background(), Config{RepoDir: theirs}, them, Snapshot{Lessons: []Lesson{{Lesson: "theirs", Ticket: "COD-1"}}}); err != nil {
		t.Fatalf("publish theirs: %v", err)
	}

	payloads, err := Fetch(context.Background(), Config{RepoDir: mine}, me.ID)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("payloads = %d, want 1 (self excluded)", len(payloads))
	}
	got := payloads[0]
	if got.Writer != them.ID || got.Name != them.Name {
		t.Errorf("attribution = %q/%q, want %q/%q", got.Writer, got.Name, them.ID, them.Name)
	}
	if len(got.Lessons) != 1 || got.Lessons[0].Lesson != "theirs" {
		t.Errorf("lessons = %+v, want the teammate's record", got.Lessons)
	}
	if ref := git(t, mine, "for-each-ref", "--format=%(refname)", TrackingPrefix); !strings.Contains(ref, them.ID) {
		t.Errorf("tracking refs = %q, want the teammate mirrored", ref)
	}
}

func TestFetchAttributesToTheRefNotThePayload(t *testing.T) {
	remote, mine := fixture(t)
	theirs := clone(t, remote, "theirs")

	them := Writer{ID: "theirs0", Name: "Grace"}
	if err := Push(context.Background(), Config{RepoDir: theirs}, them, Snapshot{Lessons: []Lesson{{Lesson: "theirs"}}}); err != nil {
		t.Fatalf("publish theirs: %v", err)
	}
	// Republish the same payload — which still names theirs0 in its body — under a
	// second ref, the shape a writer impersonating another would produce.
	if err := Push(context.Background(), Config{RepoDir: theirs}, Writer{ID: "other0", Name: "Grace"}, Snapshot{Lessons: []Lesson{{Lesson: "theirs"}}}); err != nil {
		t.Fatalf("publish under a second ref: %v", err)
	}

	payloads, err := Fetch(context.Background(), Config{RepoDir: mine}, "mine00")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	writers := map[string]bool{}
	for _, p := range payloads {
		writers[p.Writer] = true
	}
	if !writers["theirs0"] || !writers["other0"] {
		t.Errorf("writers = %v, want each ref attributed to its own segment", writers)
	}
}

func TestFetchSkipsUnreadableRefs(t *testing.T) {
	remote, mine := fixture(t)
	theirs := clone(t, remote, "theirs")

	if err := Push(context.Background(), Config{RepoDir: theirs}, Writer{ID: "good00", Name: "Grace"}, Snapshot{Lessons: []Lesson{{Lesson: "good"}}}); err != nil {
		t.Fatalf("publish good: %v", err)
	}
	// A ref in the namespace whose commit carries no payload at all.
	empty := git(t, theirs, "commit-tree", git(t, theirs, "hash-object", "-t", "tree", "--stdin", "-w"), "-m", "empty")
	git(t, theirs, "push", "--force", "origin", empty+":"+RefPrefix+"bad000")

	payloads, err := Fetch(context.Background(), Config{RepoDir: mine}, "mine00")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(payloads) != 1 || payloads[0].Writer != "good00" {
		t.Fatalf("payloads = %+v, want only the readable writer", payloads)
	}
}

func TestHasRemote(t *testing.T) {
	_, repo := fixture(t)
	if !HasRemote(context.Background(), Config{RepoDir: repo}) {
		t.Error("HasRemote = false for a repo with origin")
	}

	bare := t.TempDir()
	git(t, bare, "init", "-q")
	if HasRemote(context.Background(), Config{RepoDir: bare}) {
		t.Error("HasRemote = true for a repo with no remote")
	}
}

func TestResolveWriterReadsRepoIdentity(t *testing.T) {
	_, repo := fixture(t)
	git(t, repo, "config", "user.name", "Ada Lovelace")
	git(t, repo, "config", "user.email", "ada@example.com")

	w := ResolveWriter(context.Background(), Config{RepoDir: repo})
	if w.Name != "Ada Lovelace" || w.Email != "ada@example.com" {
		t.Errorf("identity = %q/%q, want the repo's git identity", w.Name, w.Email)
	}
	if w.ID != WriterID("ada@example.com", MachineID()) {
		t.Errorf("ID = %q, want the derived writer id", w.ID)
	}
}

// fixture builds a bare remote and one clone configured to push to it, standing in
// for a team's shared repo.
func fixture(t *testing.T) (remote, repo string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	git(t, root, "init", "--bare", "-q", remote)
	return remote, clone(t, remote, "mine")
}

func clone(t *testing.T, remote, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	git(t, t.TempDir(), "clone", "-q", remote, dir)
	git(t, dir, "config", "user.name", "Tester")
	git(t, dir, "config", "user.email", "tester@example.com")
	return dir
}

func remoteRef(t *testing.T, remote, ref string) string {
	t.Helper()
	out := git(t, remote, "for-each-ref", "--format=%(objectname)", ref)
	if out == "" {
		t.Fatalf("ref %s missing on the remote", ref)
	}
	return out
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errBuf.String())
	}
	return strings.TrimSpace(out.String())
}
