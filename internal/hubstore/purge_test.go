package hubstore

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/queue"
	"github.com/RomkaLTU/trau/internal/registry"
)

// purgeStores returns a full store set over a throwaway hub database plus the
// repo a purge runs against, so a test can seed every table the purge sweeps.
func purgeStores(t *testing.T) (*Stores, registry.Repo) {
	t.Helper()
	home := t.TempDir()
	db, err := hubdb.Open(home)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStores(home, db.SQL(), nil, Retention{}), registry.Repo{
		Name: "acme",
		Root: filepath.Join(home, "acme"),
	}
}

func countRows(t *testing.T, s *Stores, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

func TestPurgeRemovesEveryLocalTraceAndTombstones(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	issues := s.Issues()
	if _, _, err := issues.Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog", Comments: []Comment{{ExternalID: "c1", Body: "note"}}},
		{Identifier: "COD-2", Title: "Neighbour", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	if err := issues.AddRelation(root, "COD-2", "COD-1"); err != nil {
		t.Fatalf("add relation: %v", err)
	}
	sess, err := s.Grill().Create(NewGrillSession{Repo: root, IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create grill session: %v", err)
	}
	if _, _, err := s.Grill().AppendMessage(sess.ID, NewGrillMessage{Role: GrillRoleUser, Kind: GrillKindAnswer, Payload: `{}`}); err != nil {
		t.Fatalf("append grill message: %v", err)
	}
	if err := s.Grill().MarkBlockRelation(root, "COD-2", "COD-1"); err != nil {
		t.Fatalf("mark block relation: %v", err)
	}
	if _, err := s.Notifications().NotifyGrillQuestion(root, sess.ID, "COD-1", "waiting", "answer me"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	att, err := s.Attachments().Create(Attachment{
		Repo:            root,
		IssueIdentifier: "COD-1",
		Source:          AttachmentSourceLinear,
		SourceURL:       "https://uploads.example/one.png",
		SHA256:          "abc123",
		State:           AttachmentCached,
	})
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	q := s.Queue(root)
	if _, err := q.Add(queue.Item{ID: "COD-1", Kind: queue.KindEpic, SubIssues: []queue.SubIssue{{ID: "COD-7"}}}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.Pause("COD-1", "waiting"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := s.Checkpoints().Upsert(root, "COD-1", map[string]string{"PHASE": "build"}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if _, err := s.Events().Append(root, []NewEvent{{TS: "2026-07-22T10:00:00Z", Kind: "state_change", Msg: "ran"}}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	res, found, err := issues.Purge(repo, "COD-1")
	if err != nil || !found {
		t.Fatalf("purge: found=%v err=%v", found, err)
	}
	if want := []string{"COD-1"}; !slices.Equal(res.Deleted, want) {
		t.Errorf("deleted = %v, want %v", res.Deleted, want)
	}
	if want := []string{"abc123"}; !slices.Equal(res.OrphanedBlobs, want) {
		t.Errorf("orphaned blobs = %v, want %v", res.OrphanedBlobs, want)
	}

	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{"issues", `SELECT count(*) FROM issues WHERE repo = ? AND identifier = 'COD-1'`, []any{root}},
		{"issue_comments", `SELECT count(*) FROM issue_comments`, nil},
		{"issues_fts", `SELECT count(*) FROM issues_fts WHERE issues_fts MATCH '"target"*'`, nil},
		{"grill_sessions", `SELECT count(*) FROM grill_sessions WHERE repo = ?`, []any{root}},
		{"grill_messages", `SELECT count(*) FROM grill_messages`, nil},
		{"grill_relations", `SELECT count(*) FROM grill_relations WHERE repo = ?`, []any{root}},
		{"attachments", `SELECT count(*) FROM attachments WHERE id = ?`, []any{att.ID}},
		{"notifications", `SELECT count(*) FROM notifications WHERE repo = ?`, []any{root}},
		{"issue_relations", `SELECT count(*) FROM issue_relations WHERE repo = ?`, []any{root}},
		{"queue_items", `SELECT count(*) FROM queue_items WHERE root = ?`, []any{root}},
		{"queue_sub_issues", `SELECT count(*) FROM queue_sub_issues WHERE root = ?`, []any{root}},
	} {
		if n := countRows(t, s, tc.query, tc.args...); n != 0 {
			t.Errorf("%s rows = %d, want 0 after the purge", tc.name, n)
		}
	}

	if n := countRows(t, s, `SELECT count(*) FROM issues WHERE repo = ? AND identifier = 'COD-2'`, root); n != 1 {
		t.Errorf("neighbour rows = %d, want the untouched COD-2 kept", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ? AND identifier = 'COD-1'`, root); n != 1 {
		t.Errorf("tombstone rows = %d, want one for the purged synced ticket", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM checkpoints WHERE repo = ?`, root); n != 1 {
		t.Errorf("checkpoint rows = %d, want the run data left alone", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM events WHERE repo = ?`, root); n != 1 {
		t.Errorf("event rows = %d, want the run data left alone", n)
	}
}

func TestPurgeTombstoneSurvivesALaterSync(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	issues := s.Issues()
	if _, _, err := issues.Upsert(root, "linear", []Issue{{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog"}}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	if _, found, err := issues.Purge(repo, "COD-1"); err != nil || !found {
		t.Fatalf("purge: found=%v err=%v", found, err)
	}

	// The tracker payload still carries the ticket: neither the pull nor the
	// reconcile that follows it may bring the issue back.
	if _, _, err := issues.Upsert(root, "linear", []Issue{{Identifier: "COD-1", Title: "Target", StatusGroup: "backlog"}}); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if _, err := issues.Reconcile(root, []string{"COD-1"}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, found, err := issues.Get(root, "COD-1"); err != nil || found {
		t.Fatalf("get after re-sync: found=%v err=%v, want the tombstoned issue gone", found, err)
	}
	backlog, err := issues.Backlog(root)
	if err != nil {
		t.Fatalf("backlog: %v", err)
	}
	if len(backlog) != 0 {
		t.Errorf("backlog = %+v, want the tombstoned issue absent", backlog)
	}
	matches, err := issues.Search(root, "Target", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("search = %+v, want the tombstoned issue absent", matches)
	}
}

func TestPurgeEpicTakesItsChildren(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	issues := s.Issues()
	if _, _, err := issues.Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Epic", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", Title: "Slice", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-3", Title: "Slice", StatusGroup: "backlog", Parent: "COD-1"},
		{Identifier: "COD-4", Title: "Elsewhere", StatusGroup: "backlog"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}

	res, found, err := issues.Purge(repo, "COD-1")
	if err != nil || !found {
		t.Fatalf("purge: found=%v err=%v", found, err)
	}
	if want := []string{"COD-1", "COD-2", "COD-3"}; !slices.Equal(res.Deleted, want) {
		t.Fatalf("deleted = %v, want %v", res.Deleted, want)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ?`, root); n != 3 {
		t.Errorf("tombstone rows = %d, want one per deleted family member", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issues WHERE repo = ?`, root); n != 1 {
		t.Errorf("remaining issues = %d, want only COD-4 outside the family kept", n)
	}
}

func TestPurgeRefusesWhileAFamilyMemberRuns(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	issues := s.Issues()
	if _, _, err := issues.Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Epic", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", Title: "Slice", StatusGroup: "backlog", Parent: "COD-1"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	q := s.Queue(root)
	if _, err := q.Add(queue.Item{ID: "COD-2", Kind: queue.KindTicket}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := q.MarkRunning("COD-2", 4242); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	_, found, err := issues.Purge(repo, "COD-1")
	var running *IssueRunningError
	if !errors.As(err, &running) {
		t.Fatalf("purge error = %v, want an IssueRunningError", err)
	}
	if running.Identifier != "COD-2" {
		t.Errorf("running identifier = %q, want the running child", running.Identifier)
	}
	if found {
		t.Error("found = true, want the refusal to report nothing deleted")
	}
	if n := countRows(t, s, `SELECT count(*) FROM issues WHERE repo = ?`, root); n != 2 {
		t.Errorf("issues = %d, want the family untouched by the refusal", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM queue_items WHERE root = ?`, root); n != 1 {
		t.Errorf("queue items = %d, want the running entry left in place", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ?`, root); n != 0 {
		t.Errorf("tombstone rows = %d, want none written by a refusal", n)
	}
}

func TestPurgeDropsPausedAndPendingQueueEntries(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	if _, _, err := s.Issues().Upsert(root, "linear", []Issue{
		{Identifier: "COD-1", Title: "Epic", StatusGroup: "backlog", HasChildren: true},
		{Identifier: "COD-2", Title: "Slice", StatusGroup: "backlog", Parent: "COD-1"},
	}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	q := s.Queue(root)
	for _, id := range []string{"COD-1", "COD-2"} {
		if _, err := q.Add(queue.Item{ID: id, Kind: queue.KindTicket}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	if err := q.Pause("COD-1", "waiting"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	if _, found, err := s.Issues().Purge(repo, "COD-1"); err != nil || !found {
		t.Fatalf("purge: found=%v err=%v", found, err)
	}
	items, err := q.Load()
	if err != nil {
		t.Fatalf("load queue: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("queue = %+v, want the paused and pending entries removed", items)
	}
}

func TestPurgeInternalIssueLeavesNoTombstone(t *testing.T) {
	s, repo := purgeStores(t)
	root := repo.Root
	issues := s.Issues()
	iss, err := issues.CreateInternal(root, "LOOP", InternalDraft{Title: "Local only"})
	if err != nil {
		t.Fatalf("create internal: %v", err)
	}

	res, found, err := issues.Purge(repo, iss.Identifier)
	if err != nil || !found {
		t.Fatalf("purge: found=%v err=%v", found, err)
	}
	if want := []string{iss.Identifier}; !slices.Equal(res.Deleted, want) {
		t.Errorf("deleted = %v, want %v", res.Deleted, want)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issues WHERE repo = ?`, root); n != 0 {
		t.Errorf("issues = %d, want the internal issue gone", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM issue_tombstones WHERE repo = ?`, root); n != 0 {
		t.Errorf("tombstone rows = %d, want none for an internal issue", n)
	}
}

func TestPurgeUnknownIdentifierIsNotFound(t *testing.T) {
	s, repo := purgeStores(t)
	if _, _, err := s.Issues().Upsert(repo.Root, "linear", []Issue{{Identifier: "COD-1", StatusGroup: "backlog"}}); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	res, found, err := s.Issues().Purge(repo, "COD-404")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if found || len(res.Deleted) != 0 {
		t.Errorf("purge = %+v found=%v, want a miss", res, found)
	}
}
