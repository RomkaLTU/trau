package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/worktree"
)

// worktreeServer stands up a hub over a real one-commit git repository, so the
// removal paths run git rather than a stub: `git worktree remove` is the whole
// behaviour under test.
func worktreeServer(t *testing.T) (*Server, *httptest.Server, registry.Repo, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "init")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "add", "-A")
	gitCmd(t, root, "commit", "-m", "init")

	repo := registry.Repo{Name: "acme", Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}
	stores := testStoresAt(t, home)
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, repo, ts.URL + APIPrefix + "/repos/acme/worktrees"
}

// addWorktree provisions a real tree for a ticket and returns its path.
func addWorktree(t *testing.T, repo registry.Repo, dir, ticket string) string {
	t.Helper()
	res, err := worktree.Provision(context.Background(), worktree.Options{
		RepoRoot: repo.Root, Dir: dir, Repo: repo.Name, Ticket: ticket, Base: "main",
	})
	if err != nil {
		t.Fatalf("provision %s: %v", ticket, err)
	}
	return res.Path
}

func decodeWorktree(t *testing.T, res *http.Response) WorktreeView {
	t.Helper()
	defer func() { _ = res.Body.Close() }()
	var out WorktreeView
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode worktree: %v", err)
	}
	return out
}

// TestWorktreeReportThenListIsTheChildsRecord covers the child's side: it reports
// the tree it created, and the same row is what every reader sees afterwards.
func TestWorktreeReportThenListIsTheChildsRecord(t *testing.T) {
	_, _, repo, base := worktreeServer(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1581")

	res := postJSON(t, base, WorktreeRequest{Ticket: "COD-1581", Path: path, Branch: "feature/COD-1581-x"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d, want 200", res.StatusCode)
	}
	row := decodeWorktree(t, res)
	if row.State != hubstore.WorktreeActive || row.Path != path {
		t.Fatalf("reported row = %+v, want an active row at %s", row, path)
	}

	got, err := http.Get(base)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = got.Body.Close() }()
	var list []WorktreeView
	if err := json.NewDecoder(got.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Ticket != "COD-1581" || list[0].Branch != "feature/COD-1581-x" {
		t.Fatalf("list = %+v, want the one reported row", list)
	}
}

func TestWorktreeReportRejectsAnIncompleteBody(t *testing.T) {
	_, _, _, base := worktreeServer(t)

	res := postJSON(t, base, WorktreeRequest{Ticket: "COD-1", Path: ""})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

// TestWorktreeDeleteRemovesTheTreeAndSettlesTheRow is the manual Remove behind the
// run detail's button.
func TestWorktreeDeleteRemovesTheTreeAndSettlesTheRow(t *testing.T) {
	_, ts, repo, base := worktreeServer(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1581")
	row := decodeWorktree(t, postJSON(t, base, WorktreeRequest{Ticket: "COD-1581", Path: path}))

	res, body := deleteReq(t, ts, APIPrefix+"/repos/acme/worktrees/"+strconv.FormatInt(row.ID, 10))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (body %s)", res.StatusCode, body)
	}
	if !strings.Contains(body, hubstore.WorktreeSettled) {
		t.Errorf("delete body = %s, want the row settled", body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the tree survived the delete (err = %v)", err)
	}
	entries, err := worktree.List(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("git lists %d trees, want only the main checkout", len(entries))
	}
}

func TestWorktreeDeleteUnknownIDIs404(t *testing.T) {
	_, ts, _, _ := worktreeServer(t)

	res, _ := deleteReq(t, ts, APIPrefix+"/repos/acme/worktrees/9999")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

// TestSettleWorktreeRemovesTheTree is the path every hub-side settle lands on — a
// merged drain outcome, a reset, a requeue — and its no-op for the ticket that
// never had a tree, which is every run in the WORKTREES=0 world.
func TestSettleWorktreeRemovesTheTree(t *testing.T) {
	s, _, repo, base := worktreeServer(t)
	dir := filepath.Join(t.TempDir(), "worktrees")
	path := addWorktree(t, repo, dir, "COD-1581")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-1581", Path: path})

	s.settleWorktree(repo, "COD-1581")

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the tree survived the settle (err = %v)", err)
	}
	row, ok, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-1581")
	if err != nil || !ok || row.State != hubstore.WorktreeSettled {
		t.Fatalf("row after settle = (%+v, %v, %v), want settled", row, ok, err)
	}

	// A second settle, and a ticket that never had a tree, are both silent no-ops.
	s.settleWorktree(repo, "COD-1581")
	s.settleWorktree(repo, "COD-NEVER")
}

// TestReconcileWorktreesOrphansAndPrunes is the boot sweep: an active row whose
// directory vanished is orphaned, and a settled row whose directory somehow
// survived is finished off now.
func TestReconcileWorktreesOrphansAndPrunes(t *testing.T) {
	s, _, repo, base := worktreeServer(t)
	dir := filepath.Join(t.TempDir(), "worktrees")

	gone := addWorktree(t, repo, dir, "COD-GONE")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-GONE", Path: gone})
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	survivor := addWorktree(t, repo, dir, "COD-SURVIVOR")
	postJSONDiscard(t, base, WorktreeRequest{Ticket: "COD-SURVIVOR", Path: survivor})
	if _, err := s.stores.Worktrees().SettleTicket(repo.Root, "COD-SURVIVOR"); err != nil {
		t.Fatal(err)
	}

	s.reconcileWorktrees()

	row, ok, err := s.stores.Worktrees().ByTicket(repo.Root, "COD-GONE")
	if err != nil || !ok || row.State != hubstore.WorktreeOrphaned {
		t.Errorf("vanished tree's row = (%+v, %v, %v), want orphaned", row, ok, err)
	}
	if _, err := os.Stat(survivor); !os.IsNotExist(err) {
		t.Errorf("a settled row's tree survived the reconcile (err = %v)", err)
	}
	entries, err := worktree.List(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("git lists %d trees after the reconcile, want only the main checkout", len(entries))
	}
}

// TestWorktreeForOnlyNamesATreeWhenTheRepoOptedIn pins what the drain passes to a
// child: nothing at all until the repo's own config turns worktrees on.
func TestWorktreeForOnlyNamesATreeWhenTheRepoOptedIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := worktreeFor(root, "COD-1581"); got != "" {
		t.Errorf("worktreeFor without WORKTREES = %q, want empty", got)
	}

	dir := filepath.Join(t.TempDir(), "worktrees")
	cfg := "WORKTREES=1\nWORKTREES_DIR=" + dir + "\n"
	if err := os.WriteFile(filepath.Join(root, ".trau.ini"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "acme", "COD-1581")
	if got := worktreeFor(root, "COD-1581"); got != want {
		t.Errorf("worktreeFor = %q, want %q", got, want)
	}
}

func postJSONDiscard(t *testing.T, url string, body any) {
	t.Helper()
	res := postJSON(t, url, body)
	if err := res.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status = %d, want 200", url, res.StatusCode)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
