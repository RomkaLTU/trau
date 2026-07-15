package hubstore

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb"
)

func testGrill(t *testing.T, retention int) (*Grill, *sql.DB) {
	t.Helper()
	db, err := hubdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewGrill(db.SQL(), retention), db.SQL()
}

func TestGrillCreateAndMessages(t *testing.T) {
	g, _ := testGrill(t, 0)

	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1", Model: "opus"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.State != GrillRunning {
		t.Fatalf("state = %q, want running", sess.State)
	}
	if sess.ID == 0 || sess.CreatedAt == "" || sess.UpdatedAt == "" {
		t.Fatalf("session not stamped: %+v", sess)
	}

	got, found, err := g.Session(sess.ID)
	if err != nil || !found {
		t.Fatalf("session(%d): found=%v err=%v", sess.ID, found, err)
	}
	if got.IssueID != "COD-1" || got.Model != "opus" {
		t.Fatalf("session round-trip mismatch: %+v", got)
	}

	q, _, err := g.AppendMessage(sess.ID, NewGrillMessage{Role: GrillRoleAgent, Kind: GrillKindQuestion, Payload: `{"text":"why?"}`})
	if err != nil {
		t.Fatalf("append question: %v", err)
	}
	a, ok, err := g.AppendMessage(sess.ID, NewGrillMessage{Role: GrillRoleUser, Kind: GrillKindAnswer, Payload: `{"text":"because"}`})
	if err != nil || !ok {
		t.Fatalf("append answer: ok=%v err=%v", ok, err)
	}

	msgs, err := g.Messages(sess.ID, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].ID != q.ID || msgs[1].ID != a.ID {
		t.Fatalf("messages = %+v, want [%d %d]", msgs, q.ID, a.ID)
	}

	after, err := g.Messages(sess.ID, q.ID)
	if err != nil {
		t.Fatalf("messages after: %v", err)
	}
	if len(after) != 1 || after[0].ID != a.ID {
		t.Fatalf("messages after %d = %+v, want [%d]", q.ID, after, a.ID)
	}
}

func TestGrillAppendMessageUnknownSession(t *testing.T) {
	g, _ := testGrill(t, 0)
	if _, ok, err := g.AppendMessage(999, NewGrillMessage{Role: GrillRoleUser, Kind: GrillKindAnswer}); ok || err != nil {
		t.Fatalf("append to unknown session: ok=%v err=%v, want false nil", ok, err)
	}
}

func TestGrillOneActivePerIssue(t *testing.T) {
	g, _ := testGrill(t, 0)

	first, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"}); !errors.Is(err, ErrGrillActiveSession) {
		t.Fatalf("second create err = %v, want ErrGrillActiveSession", err)
	}

	// A different issue is unaffected; authoring sessions never collide.
	if _, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-2"}); err != nil {
		t.Fatalf("other issue create: %v", err)
	}
	if _, err := g.Create(NewGrillSession{Repo: "acme"}); err != nil {
		t.Fatalf("first authoring create: %v", err)
	}
	if _, err := g.Create(NewGrillSession{Repo: "acme"}); err != nil {
		t.Fatalf("second authoring create: %v", err)
	}

	// Settling the first session frees the issue for a new one.
	if _, err := g.Transition(first.ID, GrillAbandoned, ""); err != nil {
		t.Fatalf("abandon first: %v", err)
	}
	if _, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"}); err != nil {
		t.Fatalf("recreate after settle: %v", err)
	}
}

func TestGrillTransitionLegality(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := g.Transition(sess.ID, GrillApplied, ""); !errors.Is(err, ErrGrillTransition) {
		t.Fatalf("running->applied err = %v, want ErrGrillTransition", err)
	}

	steps := []struct {
		to     string
		reason string
	}{
		{GrillWaiting, ""},
		{GrillRunning, ""},
		{GrillStalled, "auth"},
		{GrillRunning, ""},
		{GrillFinished, ""},
		{GrillApplied, ""},
	}
	for _, s := range steps {
		got, err := g.Transition(sess.ID, s.to, s.reason)
		if err != nil {
			t.Fatalf("transition to %s: %v", s.to, err)
		}
		if got.State != s.to || got.ParkedReason != s.reason {
			t.Fatalf("after %s: state=%q reason=%q", s.to, got.State, got.ParkedReason)
		}
	}

	if _, err := g.Transition(sess.ID, GrillRunning, ""); !errors.Is(err, ErrGrillTransition) {
		t.Fatalf("applied->running err = %v, want ErrGrillTransition", err)
	}
	if _, err := g.Transition(9999, GrillWaiting, ""); !errors.Is(err, ErrGrillNotFound) {
		t.Fatalf("unknown transition err = %v, want ErrGrillNotFound", err)
	}
}

func TestGrillFinishedReopens(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := g.Transition(sess.ID, GrillFinished, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	got, err := g.Transition(sess.ID, GrillRunning, "")
	if err != nil {
		t.Fatalf("finished->running: %v", err)
	}
	if got.State != GrillRunning {
		t.Fatalf("state = %q, want %q", got.State, GrillRunning)
	}

	// The reopened session still settles the same way a first-pass one does.
	if _, err := g.Transition(sess.ID, GrillFinished, ""); err != nil {
		t.Fatalf("refinish: %v", err)
	}
	if _, err := g.Transition(sess.ID, GrillApplied, ""); err != nil {
		t.Fatalf("apply after reopen: %v", err)
	}
}

func TestGrillUpdateChain(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := g.UpdateChain(sess.ID, "sid-abc")
	if err != nil || !ok {
		t.Fatalf("update chain: ok=%v err=%v", ok, err)
	}
	if got.SessionChain != "sid-abc" {
		t.Fatalf("session_chain = %q, want sid-abc", got.SessionChain)
	}
	if _, ok, _ := g.UpdateChain(9999, "x"); ok {
		t.Fatalf("update chain on unknown session reported ok")
	}
}

func TestGrillSetIssue(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme"})
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	if sess.IssueID != "" {
		t.Fatalf("new authoring session issue = %q, want empty", sess.IssueID)
	}

	updated, found, err := g.SetIssue(sess.ID, "COD-9")
	if err != nil || !found {
		t.Fatalf("set issue: found=%v err=%v", found, err)
	}
	if updated.IssueID != "COD-9" {
		t.Fatalf("anchored issue = %q, want COD-9", updated.IssueID)
	}

	got, _, err := g.Session(sess.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got.IssueID != "COD-9" {
		t.Fatalf("persisted issue = %q, want COD-9", got.IssueID)
	}

	if _, found, err := g.SetIssue(9999, "COD-1"); found || err != nil {
		t.Fatalf("set issue on unknown session = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestGrillListFilter(t *testing.T) {
	g, _ := testGrill(t, 0)
	a, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	b, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-2"})
	if _, err := g.Create(NewGrillSession{Repo: "other", IssueID: "COD-3"}); err != nil {
		t.Fatalf("other repo create: %v", err)
	}

	all, err := g.List("acme", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].ID != b.ID || all[1].ID != a.ID {
		t.Fatalf("list newest-first = %+v", all)
	}

	if _, err := g.Transition(b.ID, GrillWaiting, ""); err != nil {
		t.Fatalf("transition: %v", err)
	}
	waiting, err := g.List("acme", GrillWaiting)
	if err != nil {
		t.Fatalf("list waiting: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != b.ID {
		t.Fatalf("list waiting = %+v, want [%d]", waiting, b.ID)
	}
}

func TestGrillReadsIssueTitle(t *testing.T) {
	g, db := testGrill(t, 0)
	if _, _, err := NewIssues(db).Upsert("acme", "linear", []Issue{
		{Identifier: "COD-1", Title: "Split the picker"},
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	grilled, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	authoring, _ := g.Create(NewGrillSession{Repo: "acme"})
	untracked, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-404"})

	sess, found, err := g.Session(grilled.ID)
	if err != nil || !found {
		t.Fatalf("session(%d) = %v, %v", grilled.ID, found, err)
	}
	if sess.IssueTitle != "Split the picker" {
		t.Fatalf("session issue title = %q, want %q", sess.IssueTitle, "Split the picker")
	}

	list, err := g.List("acme", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	titles := map[int64]string{}
	for _, s := range list {
		titles[s.ID] = s.IssueTitle
	}
	if titles[grilled.ID] != "Split the picker" {
		t.Fatalf("listed issue title = %q, want %q", titles[grilled.ID], "Split the picker")
	}
	// An authoring session anchors to the repo alone, and a session can outlive the
	// issue row it names — both keep an empty title rather than dropping the row.
	if titles[authoring.ID] != "" || titles[untracked.ID] != "" {
		t.Fatalf("titleless sessions = %q, %q, want empty", titles[authoring.ID], titles[untracked.ID])
	}
	if len(list) != 3 {
		t.Fatalf("list = %d sessions, want 3", len(list))
	}
}

func TestGrillPruneKeepsRecentSettled(t *testing.T) {
	g, db := testGrill(t, 2)

	var ids []int64
	for i := 0; i < 4; i++ {
		s, err := g.Create(NewGrillSession{Repo: "acme"})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, s.ID)
	}
	// Settle the two oldest so they are prunable; the two newest stay.
	for _, id := range ids[:2] {
		if _, err := db.Exec(`UPDATE grill_sessions SET state = 'abandoned' WHERE id = ?`, id); err != nil {
			t.Fatalf("settle %d: %v", id, err)
		}
	}
	if err := g.Prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}
	remaining, err := g.List("acme", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(remaining) != 2 || remaining[0].ID != ids[3] || remaining[1].ID != ids[2] {
		t.Fatalf("after prune = %+v, want newest two %d %d", remaining, ids[3], ids[2])
	}
}

func TestGrillPruneKeepsActiveBeyondWindow(t *testing.T) {
	g, db := testGrill(t, 1)
	old, _ := g.Create(NewGrillSession{Repo: "acme"})
	if _, err := db.Exec(`UPDATE grill_sessions SET state = 'waiting' WHERE id = ?`, old.ID); err != nil {
		t.Fatalf("mark active: %v", err)
	}
	for i := 0; i < 3; i++ {
		s, _ := g.Create(NewGrillSession{Repo: "acme"})
		if _, err := db.Exec(`UPDATE grill_sessions SET state = 'abandoned' WHERE id = ?`, s.ID); err != nil {
			t.Fatalf("settle: %v", err)
		}
	}
	if err := g.Prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, found, err := g.Session(old.ID); err != nil || !found {
		t.Fatalf("active session pruned: found=%v err=%v", found, err)
	}
}

func TestGrillSweepIdle(t *testing.T) {
	g, db := testGrill(t, 0)
	stale, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	fresh, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-2"})
	settled, _ := g.Create(NewGrillSession{Repo: "acme"})

	old := formatGrillTime(time.Now().Add(-40 * 24 * time.Hour))
	if _, err := db.Exec(`UPDATE grill_sessions SET state = 'parked', updated_at = ? WHERE id = ?`, old, stale.ID); err != nil {
		t.Fatalf("backdate stale: %v", err)
	}
	if _, err := db.Exec(`UPDATE grill_sessions SET state = 'applied', updated_at = ? WHERE id = ?`, old, settled.ID); err != nil {
		t.Fatalf("backdate settled: %v", err)
	}

	swept, err := g.SweepIdle(time.Now().Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(swept) != 1 || swept[0].ID != stale.ID || swept[0].State != GrillAbandoned {
		t.Fatalf("swept = %+v, want stale abandoned", swept)
	}

	if got, _, _ := g.Session(fresh.ID); got.State != GrillRunning {
		t.Fatalf("fresh session state = %q, want running", got.State)
	}
	if got, _, _ := g.Session(settled.ID); got.State != GrillApplied {
		t.Fatalf("settled session state = %q, want applied", got.State)
	}
}
