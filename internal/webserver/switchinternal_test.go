package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// switchJiraINI is a repo pointed at Jira with credentials in its own file — the state
// a switch to the internal tracker has to leave untouched.
const switchJiraINI = "TRACKER_PROVIDER=jira\n" +
	"JIRA_BASE_URL=https://acme.atlassian.net\n" +
	"JIRA_EMAIL=dev@acme.io\n" +
	"JIRA_API_TOKEN=jira-secret\n" +
	"JIRA_PROJECT=ACME\n"

// switchRepo creates a registered repo root under home carrying ini, and returns
// the root.
func switchRepo(t *testing.T, home, name, ini string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	writeRepoINI(t, root, ini)
	repo := registry.Repo{Name: name, Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember %s: %v", name, err)
	}
	return root
}

// switchServer builds a hub over home whose reader factory answers with fake, so
// a test can drive the settings writes and then a real sync against it.
func switchServer(t *testing.T, home string, fake tracker.Reader) *httptest.Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	if fake != nil {
		s.newReader = func(config.Config) (tracker.Reader, error) { return fake, nil }
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// seedJiraMirror stores the synced rows, comment, blocked-by edge, binding and
// cursor a repo mirroring Jira carries — including a ticket whose identifier
// shares the repo's internal prefix, the collision the seq floor exists for.
func seedJiraMirror(t *testing.T, home, root string) {
	t.Helper()
	store := testStoresAt(t, home).Issues()
	if _, _, err := store.Upsert(root, "jira", []hubstore.Issue{
		{Identifier: "ACME-1", Title: "First", StatusGroup: "started", ExternalID: "j-1", URL: "https://acme.atlassian.net/browse/ACME-1",
			Comments: []hubstore.Comment{{ExternalID: "c1", Author: "Ada", Body: "looks good"}}},
		{Identifier: "ACME-2", Title: "Second", StatusGroup: "unstarted", ExternalID: "j-2"},
		{Identifier: "ACME-42", Title: "Highest", StatusGroup: "unstarted", ExternalID: "j-42"},
	}); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	if err := store.AddRelation(root, "ACME-1", "ACME-2"); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
	if err := store.SaveBinding(root, hubstore.SyncBinding{ProjectID: "10001", Project: "ACME", Target: "jira:ACME"}); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if err := store.RecordResult(root, hubstore.SyncResult{Issues: 3, Comments: 1, Cursor: "2026-08-01T00:00:00Z", SyncedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
}

// seedQueueHistory queues a ticket under root and settles it with status, so the
// repo carries queue history for an identifier no longer in the store.
func seedQueueHistory(t *testing.T, home, root, id, source, status string) {
	t.Helper()
	q := testStoresAt(t, home).Queue(root)
	if _, err := q.Add(queue.Item{Kind: queue.KindTicket, ID: id, Title: id, Source: source}); err != nil {
		t.Fatalf("queue %s: %v", id, err)
	}
	if status == queue.StatusPending {
		return
	}
	if err := q.Finish(id, status, ""); err != nil {
		t.Fatalf("settle %s: %v", id, err)
	}
}

// commentCount counts the comment rows the hub database under home still holds
// for root, the cascade a mirror drop is supposed to take with it.
func commentCount(t *testing.T, home, root string) int {
	t.Helper()
	db, err := hubdbtest.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var n int
	if err := db.SQL().QueryRow(
		`SELECT count(*) FROM issue_comments c JOIN issues i ON i.id = c.issue_id WHERE i.repo = ?`, root,
	).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n
}

// mintInternal files an internal issue the way the hub does, returning the
// identifier the sequence handed out.
func mintInternal(t *testing.T, home, root, title string) string {
	t.Helper()
	iss, err := testStoresAt(t, home).Issues().CreateInternal(root, "ACME", hubstore.InternalDraft{Title: title})
	if err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	return iss.Identifier
}

func identifiers(issues []hubstore.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, iss := range issues {
		out = append(out, iss.Identifier)
	}
	return out
}

// Pointing a mirroring repo at the internal tracker retires the mirror rather
// than stranding it beside hub-only issues, and the identifier sequence clears
// every id the mirror and the queue history spoke for. Credentials and the cached
// binding survive — they are what a switch back needs.
func TestSwitchToInternalDropsMirrorAndAdvancesSeq(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := switchRepo(t, home, "acme", switchJiraINI)
	seedJiraMirror(t, home, root)
	seedQueueHistory(t, home, root, "ACME-55", "jira", queue.StatusDone)
	internal := mintInternal(t, home, root, "Hub-only work")

	ts := switchServer(t, home, nil)
	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "internal", Layer: "project"})
	if body := readBody(t, res); res.StatusCode != http.StatusOK {
		t.Fatalf("put config = %d (%s)", res.StatusCode, body)
	}

	store := testStoresAt(t, home).Issues()
	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := identifiers(stored); len(got) != 1 || got[0] != internal {
		t.Fatalf("store holds %v, want only the internal issue %s", got, internal)
	}
	if n := commentCount(t, home, root); n != 0 {
		t.Errorf("comments = %d, want 0 — the mirror's comments cascade with it", n)
	}
	if blockers, err := store.Blockers(root, "ACME-2"); err != nil || len(blockers) != 0 {
		t.Errorf("blocked-by edges = %v (err=%v), want none left dangling", blockers, err)
	}

	state, err := store.SyncState(root)
	if err != nil {
		t.Fatalf("SyncState: %v", err)
	}
	if state.Cursor != "" {
		t.Errorf("cursor = %q, want empty so a switch back re-pulls everything", state.Cursor)
	}
	if state.Binding.ProjectID != "10001" || state.Binding.Project != "ACME" {
		t.Errorf("binding = %+v, want the cached Jira binding kept", state.Binding)
	}

	ini := repoINI(t, root)
	if ini["TRACKER_PROVIDER"] != "internal" {
		t.Errorf("TRACKER_PROVIDER = %q, want internal", ini["TRACKER_PROVIDER"])
	}
	for _, key := range []string{"JIRA_BASE_URL", "JIRA_EMAIL", "JIRA_API_TOKEN", "JIRA_PROJECT"} {
		if ini[key] == "" {
			t.Errorf("%s was cleared; the switch must leave credentials alone (file %v)", key, ini)
		}
	}

	// ACME-55 only ever existed in the queue, and ACME-42 has just been dropped —
	// the sequence still has to clear both.
	if next := mintInternal(t, home, root, "After the switch"); next != "ACME-56" {
		t.Fatalf("next internal id = %s, want ACME-56 — past every id the mirror and queue history held", next)
	}
}

// A project switches its members as one: the tracker PUT is the same migration,
// applied to every member repo that still carries a mirror.
func TestProjectTrackerSwitchToInternalMigratesEveryMember(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}
	if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
		"TRACKER_PROVIDER": "jira",
		"JIRA_BASE_URL":    "https://acme.atlassian.net",
		"JIRA_EMAIL":       "dev@acme.io",
		"JIRA_API_TOKEN":   "jira-secret",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("seed tracker = %d (%s)", res.StatusCode, body)
	}

	store := testStoresAt(t, home).Issues()
	for _, root := range []string{api, web} {
		if _, _, err := store.Upsert(root, "jira", []hubstore.Issue{
			{Identifier: "COD-1", Title: "Mirrored", StatusGroup: "unstarted", ExternalID: "j-1"},
		}); err != nil {
			t.Fatalf("seed %s mirror: %v", root, err)
		}
	}

	if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{"TRACKER_PROVIDER": "internal"}); res.StatusCode != http.StatusOK {
		t.Fatalf("switch = %d (%s)", res.StatusCode, body)
	}

	for _, root := range []string{api, web} {
		stored, err := store.List(root)
		if err != nil {
			t.Fatalf("List %s: %v", root, err)
		}
		if len(stored) != 0 {
			t.Errorf("%s still mirrors %v, want the mirror dropped", root, identifiers(stored))
		}
		wantINI(t, root, map[string]string{"TRACKER_PROVIDER": "internal", "JIRA_API_TOKEN": "jira-secret"})
	}
}

// Live tracker work in the queue is held under an identifier the drop would
// retire, so the whole write is refused — with the blocking id named — and
// nothing moves.
func TestSwitchToInternalRefusedWhileQueueHoldsTrackerWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := switchRepo(t, home, "acme", switchJiraINI)
	seedJiraMirror(t, home, root)
	seedQueueHistory(t, home, root, "ACME-2", "jira", queue.StatusPending)

	ts := switchServer(t, home, nil)
	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "internal", Layer: "project"})
	body := readBody(t, res)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("put config = %d (%s), want 409", res.StatusCode, body)
	}
	if !strings.Contains(body, "ACME-2") || !strings.Contains(body, "pending") {
		t.Errorf("refusal %q does not name the blocking entry", body)
	}
	var refusal struct {
		Reason  string                 `json:"reason"`
		Blocked []TrackerSwitchBlocker `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(body), &refusal); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if refusal.Reason != "queue_busy" || len(refusal.Blocked) != 1 || refusal.Blocked[0].ID != "ACME-2" {
		t.Fatalf("refusal = %+v, want a structured queue_busy naming ACME-2", refusal)
	}

	stored, err := testStoresAt(t, home).Issues().List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 3 {
		t.Errorf("store holds %v, want the mirror untouched", identifiers(stored))
	}
	if got := repoINI(t, root)["TRACKER_PROVIDER"]; got != "jira" {
		t.Errorf("TRACKER_PROVIDER = %q, want jira — a refused write writes nothing", got)
	}
}

// With no mirror to retire the switch is a plain key write: the internal issues
// stay put and the sequence is left where it was.
func TestSwitchToInternalWithoutMirrorIsAPlainWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := switchRepo(t, home, "acme", switchJiraINI)
	first := mintInternal(t, home, root, "Hub-only work")
	seedQueueHistory(t, home, root, "ACME-55", "jira", queue.StatusDone)

	ts := switchServer(t, home, nil)
	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "internal", Layer: "project"})
	if body := readBody(t, res); res.StatusCode != http.StatusOK {
		t.Fatalf("put config = %d (%s)", res.StatusCode, body)
	}

	stored, err := testStoresAt(t, home).Issues().List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := identifiers(stored); len(got) != 1 || got[0] != first {
		t.Fatalf("store holds %v, want the internal issue %s", got, first)
	}
	if got := repoINI(t, root)["TRACKER_PROVIDER"]; got != "internal" {
		t.Errorf("TRACKER_PROVIDER = %q, want internal", got)
	}
	// No mirror, no migration: the sequence keeps counting from where it was.
	if next := mintInternal(t, home, root, "Next"); next != "ACME-2" {
		t.Errorf("next internal id = %s, want ACME-2 — no drop means no seq advance", next)
	}
}

// The switch is reversible because nothing is tombstoned and the cursor is empty:
// pointing the repo back at Jira re-imports the whole project, and the ids minted
// while it was internal sit above the ones coming back.
func TestSwitchBackReimportsTheWholeProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := switchRepo(t, home, "acme", switchJiraINI)
	fake := &fakeReader{synced: []tracker.SyncedIssue{
		{ID: "ACME-1", ExternalID: "j-1", Title: "First", Group: tracker.StatusGroupStarted, UpdatedAt: "2026-08-01T00:00:00Z",
			Comments: []tracker.SyncedComment{{ExternalID: "c1", Author: "Ada", Body: "looks good"}}},
		{ID: "ACME-42", ExternalID: "j-42", Title: "Highest", Group: tracker.StatusGroupUnstarted, UpdatedAt: "2026-08-01T00:00:00Z"},
	}}
	ts := switchServer(t, home, fake)

	if res, out := postSync(t, ts, "acme"); res.StatusCode != http.StatusOK || out.Issues != 2 {
		t.Fatalf("initial sync = %d (%+v)", res.StatusCode, out)
	}

	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "internal", Layer: "project"})
	if body := readBody(t, res); res.StatusCode != http.StatusOK {
		t.Fatalf("switch to internal = %d (%s)", res.StatusCode, body)
	}
	minted := mintInternal(t, home, root, "Filed while internal")
	if minted != "ACME-43" {
		t.Fatalf("minted %s, want ACME-43 — clear of the ids the mirror held", minted)
	}

	back := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "jira", Layer: "project"})
	if body := readBody(t, back); back.StatusCode != http.StatusOK {
		t.Fatalf("switch back = %d (%s)", back.StatusCode, body)
	}
	resync, out := postSync(t, ts, "acme")
	if resync.StatusCode != http.StatusOK || out.Issues != 2 || out.Comments != 1 {
		t.Fatalf("re-sync = %d (%+v), want the whole project re-imported", resync.StatusCode, out)
	}
	if fake.syncSince != "" {
		t.Errorf("re-pull cursor = %q, want empty so nothing is skipped", fake.syncSince)
	}

	store := testStoresAt(t, home).Issues()
	stored, err := store.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := identifiers(stored)
	if len(got) != 3 {
		t.Fatalf("store holds %v, want both re-imported tickets beside the internal issue", got)
	}
	for _, want := range []string{"ACME-1", "ACME-42", minted} {
		if !slices.Contains(got, want) {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	kept, found, err := store.Internal(root, minted)
	if err != nil || !found || kept.Title != "Filed while internal" {
		t.Errorf("internal %s = %+v (found=%v err=%v), want the re-import to have left it alone", minted, kept, found, err)
	}
}
