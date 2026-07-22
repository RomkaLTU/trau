package webserver

import (
	"errors"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// waitForCleanups blocks until the fake supervisor has recorded n children, the
// signal that the background purge cleanup ran, and returns their argument vectors.
func waitForCleanups(t *testing.T, fake *fakeSupervisor, n int) [][]string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if args := cleanupArgs(fake); len(args) >= n {
			return args
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cleanup ran %d children, want %d", len(cleanupArgs(fake)), n)
	return nil
}

func cleanupArgs(fake *fakeSupervisor) [][]string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	args := make([][]string, 0, len(fake.captures))
	for _, spec := range fake.captures {
		args = append(args, spec.Args)
	}
	return args
}

func TestDeleteIssueDropsTheTicketsBranchAndRunsState(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})
	fake := s.sup.(*fakeSupervisor)

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	got := waitForCleanups(t, fake, 1)[0]
	want := []string{"--repo", root, "--reset-local", "COD-1", "--no-tui"}
	if !slices.Equal(got, want) {
		t.Errorf("cleanup args = %v, want %v", got, want)
	}
	fake.mu.Lock()
	dir := fake.captures[0].Dir
	fake.mu.Unlock()
	if dir != root {
		t.Errorf("cleanup dir = %q, want the target repo %q", dir, root)
	}
}

func TestDeleteEpicCleansEveryPurgedChild(t *testing.T) {
	s, _, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", StatusGroup: "backlog", Parent: "COD-1"},
	})
	fake := s.sup.(*fakeSupervisor)

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	cleaned := []string{}
	for _, args := range waitForCleanups(t, fake, 3) {
		cleaned = append(cleaned, args[slices.Index(args, "--reset-local")+1])
	}
	if want := []string{"COD-1", "COD-2", "COD-3"}; !slices.Equal(cleaned, want) {
		t.Errorf("cleaned = %v, want the epic and both children", cleaned)
	}
}

func TestDeleteIssueLeavesTheGitFootprintWhileTheRepoIsBusy(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{
		{Identifier: "COD-1", StatusGroup: "backlog"},
		{Identifier: "COD-2", StatusGroup: "backlog"},
	})
	fake := s.sup.(*fakeSupervisor)
	writeInstanceEntry(t, s, registry.Entry{
		PID:          os.Getpid(),
		RepoRoot:     root,
		SessionState: registry.StateWorking,
		Ticket:       "COD-2",
	})

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the purge stands even when the cleanup is skipped", res.StatusCode)
	}
	if _, found, err := s.stores.Issues().Get(root, "COD-1"); err != nil || found {
		t.Errorf("COD-1: found=%v err=%v, want the row gone", found, err)
	}

	time.Sleep(50 * time.Millisecond)
	if args := cleanupArgs(fake); len(args) != 0 {
		t.Errorf("cleanup ran %v, want nothing while COD-2 holds the working tree", args)
	}
}

func TestDeleteIssueSucceedsWhenTheGitCleanupFails(t *testing.T) {
	s, root, ts := archiveServer(t, []hubstore.Issue{{Identifier: "COD-1", StatusGroup: "backlog"}})
	fake := s.sup.(*fakeSupervisor)
	fake.captureErr = errors.New("chdir " + root + ": no such file or directory")

	res := doReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/acme/issues/COD-1", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the DB purge is authoritative", res.StatusCode)
	}
	if deleted := deleteIssueResponse(t, res).Deleted; !slices.Equal(deleted, []string{"COD-1"}) {
		t.Errorf("deleted = %v, want the response shape unchanged", deleted)
	}
	waitForCleanups(t, fake, 1)
	if _, found, err := s.stores.Issues().Get(root, "COD-1"); err != nil || found {
		t.Errorf("COD-1: found=%v err=%v, want the row gone", found, err)
	}
}
