package hubstore

import (
	"database/sql"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testNotifications(t *testing.T) (*Notifications, *sql.DB) {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewNotifications(db.SQL()), db.SQL()
}

func TestNotifyGrillQuestionCoalesces(t *testing.T) {
	n, _ := testNotifications(t)

	first, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "Grilling needs you — COD-1", "why split it?")
	if err != nil {
		t.Fatalf("first notify: %v", err)
	}
	if first.ID == 0 || first.ReadAt != "" {
		t.Fatalf("first notification not stamped unread: %+v", first)
	}

	second, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "Grilling needs you — COD-1", "or rewrite it?")
	if err != nil {
		t.Fatalf("second notify: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second notification id = %d, want coalesced onto %d", second.ID, first.ID)
	}
	if second.Body != "or rewrite it?" {
		t.Fatalf("coalesced body = %q, want the newer question", second.Body)
	}

	list, err := n.List(100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d notifications, want 1 coalesced row", len(list))
	}
	if unread, _ := n.UnreadCount(); unread != 1 {
		t.Fatalf("unread = %d, want 1", unread)
	}
}

func TestNotifyGrillQuestionKeepsBodyWhenEmpty(t *testing.T) {
	n, _ := testNotifications(t)
	if _, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "title", "the question"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	// A later transition with no fresh text (a park after the question) must keep the
	// stored question rather than blank it.
	bumped, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "title", "")
	if err != nil {
		t.Fatalf("bump: %v", err)
	}
	if bumped.Body != "the question" {
		t.Fatalf("body after empty bump = %q, want the stored question", bumped.Body)
	}
}

func TestNotifyGrillQuestionInsertsAfterRead(t *testing.T) {
	n, _ := testNotifications(t)
	first, _ := n.NotifyGrillQuestion("acme", 7, "COD-1", "title", "q1")
	if err := n.ResolveGrillQuestion(7); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// With the previous one read, a new question is a fresh unread row, not a bump.
	second, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "title", "q2")
	if err != nil {
		t.Fatalf("notify after read: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("notification reused read row %d instead of inserting", first.ID)
	}
	if unread, _ := n.UnreadCount(); unread != 1 {
		t.Fatalf("unread = %d, want 1", unread)
	}
	if list, _ := n.List(100); len(list) != 2 {
		t.Fatalf("list = %d, want 2 (one read, one unread)", len(list))
	}
}

func TestResolveGrillQuestion(t *testing.T) {
	n, _ := testNotifications(t)
	if _, err := n.NotifyGrillQuestion("acme", 7, "COD-1", "title", "q"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if err := n.ResolveGrillQuestion(7); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if unread, _ := n.UnreadCount(); unread != 0 {
		t.Fatalf("unread after resolve = %d, want 0", unread)
	}
	// Resolving a session with nothing unread is a no-op, not an error.
	if err := n.ResolveGrillQuestion(999); err != nil {
		t.Fatalf("resolve unknown session: %v", err)
	}
}

func TestNotifyRunAttentionInserts(t *testing.T) {
	n, _ := testNotifications(t)
	a, err := n.NotifyRunAttention("acme", NotificationRunPaused, "COD-2", "COD-2", "Run paused — acme", "usage window")
	if err != nil {
		t.Fatalf("first run notify: %v", err)
	}
	// Each pause/fault/quarantine is a distinct fact — never coalesced.
	b, err := n.NotifyRunAttention("acme", NotificationRunPaused, "COD-2", "COD-2", "Run paused — acme", "usage window")
	if err != nil {
		t.Fatalf("second run notify: %v", err)
	}
	if a.ID == b.ID {
		t.Fatalf("run notifications coalesced onto %d, want distinct rows", a.ID)
	}
	if unread, _ := n.UnreadCount(); unread != 2 {
		t.Fatalf("unread = %d, want 2", unread)
	}
}

func TestNotificationsMarkReadAndAll(t *testing.T) {
	n, _ := testNotifications(t)
	one, _ := n.NotifyRunAttention("acme", NotificationRunFaulted, "COD-1", "COD-1", "t", "b")
	if _, err := n.NotifyRunAttention("acme", NotificationRunFaulted, "COD-2", "COD-2", "t", "b"); err != nil {
		t.Fatalf("notify: %v", err)
	}

	if err := n.MarkRead(one.ID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if unread, _ := n.UnreadCount(); unread != 1 {
		t.Fatalf("unread after one read = %d, want 1", unread)
	}

	if err := n.MarkAllRead(); err != nil {
		t.Fatalf("mark all read: %v", err)
	}
	if unread, _ := n.UnreadCount(); unread != 0 {
		t.Fatalf("unread after all read = %d, want 0", unread)
	}
}

func TestNotificationsListNewestFirst(t *testing.T) {
	n, _ := testNotifications(t)
	first, _ := n.NotifyRunAttention("acme", NotificationRunPaused, "COD-1", "COD-1", "t", "b")
	second, _ := n.NotifyRunAttention("acme", NotificationRunPaused, "COD-2", "COD-2", "t", "b")

	list, err := n.List(100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list = %+v, want newest %d then %d", list, second.ID, first.ID)
	}
}

func TestNotificationsRetentionPrunesRead(t *testing.T) {
	n, db := testNotifications(t)

	// One unread row that must survive pruning regardless of age.
	survivor, err := n.NotifyGrillQuestion("acme", 1, "COD-1", "t", "keep me")
	if err != nil {
		t.Fatalf("unread notify: %v", err)
	}

	// Fill well past the read retention window with read run notifications.
	total := notificationReadRetention + 25
	for i := 0; i < total; i++ {
		row, err := n.NotifyRunAttention("acme", NotificationRunFaulted, "COD-x", "COD-x", "t", "b")
		if err != nil {
			t.Fatalf("run notify %d: %v", i, err)
		}
		if err := n.MarkRead(row.ID); err != nil {
			t.Fatalf("mark read %d: %v", i, err)
		}
	}
	// Pruning runs on insert, so one more notification settles the last read row into
	// the window; the trigger itself is unread and never prunable.
	if _, err := n.NotifyRunAttention("acme", NotificationRunQuarantined, "COD-9", "COD-9", "t", "b"); err != nil {
		t.Fatalf("prune trigger: %v", err)
	}

	var read int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE read_at IS NOT NULL`).Scan(&read); err != nil {
		t.Fatalf("count read: %v", err)
	}
	if read != notificationReadRetention {
		t.Fatalf("read rows after prune = %d, want %d", read, notificationReadRetention)
	}

	if got, err := n.byID(survivor.ID); err != nil || got.ReadAt != "" {
		t.Fatalf("unread survivor byID = (%+v, %v), want present and unread", got, err)
	}
	// The two unread rows — the grill survivor and the prune trigger — both persist.
	if unread, _ := n.UnreadCount(); unread != 2 {
		t.Fatalf("unread after prune = %d, want 2", unread)
	}
}
