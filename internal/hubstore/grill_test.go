package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubdb/hubdbtest"
)

func testGrill(t *testing.T, retention int) (*Grill, *sql.DB) {
	t.Helper()
	db, err := hubdbtest.Open(t.TempDir())
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

func TestGrillSetModel(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1", Model: "sonnet"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, found, err := g.SetModel(sess.ID, "opus")
	if err != nil || !found {
		t.Fatalf("set model: found=%v err=%v", found, err)
	}
	if updated.Model != "opus" {
		t.Fatalf("model = %q, want opus", updated.Model)
	}

	got, _, err := g.Session(sess.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got.Model != "opus" {
		t.Fatalf("persisted model = %q, want opus", got.Model)
	}

	if _, found, err := g.SetModel(9999, "opus"); found || err != nil {
		t.Fatalf("set model on unknown session = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestGrillSetAutoAccept(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.AutoAccept {
		t.Fatalf("created session = %+v, want auto-accept off", sess)
	}

	updated, found, err := g.SetAutoAccept(sess.ID, true)
	if err != nil || !found {
		t.Fatalf("set auto-accept: found=%v err=%v", found, err)
	}
	if !updated.AutoAccept {
		t.Fatal("returned session still reports auto-accept off")
	}
	if got, _, _ := g.Session(sess.ID); !got.AutoAccept {
		t.Fatal("persisted session still reports auto-accept off")
	}

	if _, _, err := g.SetAutoAccept(sess.ID, false); err != nil {
		t.Fatalf("clear auto-accept: %v", err)
	}
	if got, _, _ := g.Session(sess.ID); got.AutoAccept {
		t.Fatal("persisted session still reports auto-accept on")
	}

	if _, found, err := g.SetAutoAccept(9999, true); found || err != nil {
		t.Fatalf("set auto-accept on unknown session = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestGrillInterjectionQueue(t *testing.T) {
	tests := []struct {
		name    string
		queue   []string
		consume int
		want    []string
	}{
		{name: "nothing queued", want: []string{}},
		{name: "one interjection", queue: []string{"drop the schema thread"}, want: []string{"drop the schema thread"}},
		{name: "multiple deliver in order", queue: []string{"first", "second", "third"}, want: []string{"first", "second", "third"}},
		{
			name:    "a second consume takes nothing",
			queue:   []string{"first", "second"},
			consume: 1,
			want:    []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, _ := testGrill(t, 0)
			sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Answers and questions share the queue's table and must never be claimed by it.
			for _, text := range tt.queue {
				if _, _, err := g.AppendMessage(sess.ID, NewGrillMessage{
					Role: GrillRoleAgent, Kind: GrillKindQuestion, Payload: `{"text":"why?"}`,
				}); err != nil {
					t.Fatalf("append question: %v", err)
				}
				if _, _, err := g.AppendMessage(sess.ID, NewGrillMessage{
					Role: GrillRoleUser, Kind: GrillKindInterjection, Payload: `{"text":"` + text + `"}`,
				}); err != nil {
					t.Fatalf("append interjection: %v", err)
				}
			}
			if _, _, err := g.AppendMessage(sess.ID, NewGrillMessage{
				Role: GrillRoleUser, Kind: GrillKindAnswer, Payload: `{"text":"an answer"}`,
			}); err != nil {
				t.Fatalf("append answer: %v", err)
			}
			for range tt.consume {
				if _, err := g.ConsumeInterjections(sess.ID); err != nil {
					t.Fatalf("prior consume: %v", err)
				}
			}

			pending, err := g.PendingInterjections(sess.ID)
			if err != nil {
				t.Fatalf("pending: %v", err)
			}
			if got := interjectionTexts(pending); !slices.Equal(got, tt.want) {
				t.Errorf("pending = %q, want %q", got, tt.want)
			}
			claimed, err := g.ConsumeInterjections(sess.ID)
			if err != nil {
				t.Fatalf("consume: %v", err)
			}
			if got := interjectionTexts(claimed); !slices.Equal(got, tt.want) {
				t.Errorf("consumed = %q, want %q", got, tt.want)
			}
			again, err := g.ConsumeInterjections(sess.ID)
			if err != nil {
				t.Fatalf("second consume: %v", err)
			}
			if len(again) != 0 {
				t.Errorf("second consume = %q, want nothing left", interjectionTexts(again))
			}
		})
	}
}

// The cursor is a column, not runner memory, so a queue that outlives the process that
// took the message is still delivered — once — by whatever store reads it next.
func TestGrillInterjectionsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	db, err := hubdbtest.Open(dir)
	if err != nil {
		t.Fatalf("open hub db: %v", err)
	}
	g := NewGrill(db.SQL(), 0)
	sess, err := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, _, err := g.AppendMessage(sess.ID, NewGrillMessage{
		Role: GrillRoleUser, Kind: GrillKindInterjection, Payload: `{"text":"use postgres"}`,
	}); err != nil {
		t.Fatalf("append interjection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close hub db: %v", err)
	}

	reopened, err := hubdbtest.Open(dir)
	if err != nil {
		t.Fatalf("reopen hub db: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	g = NewGrill(reopened.SQL(), 0)

	claimed, err := g.ConsumeInterjections(sess.ID)
	if err != nil {
		t.Fatalf("consume after reopen: %v", err)
	}
	if got := interjectionTexts(claimed); !slices.Equal(got, []string{"use postgres"}) {
		t.Fatalf("consumed = %q, want the queued interjection", got)
	}
	again, err := g.ConsumeInterjections(sess.ID)
	if err != nil {
		t.Fatalf("second consume after reopen: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second consume = %q, want nothing left", interjectionTexts(again))
	}
}

func interjectionTexts(msgs []GrillMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(m.Payload), &p)
		out = append(out, p.Text)
	}
	return out
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

	updated, found, err := g.SetIssue(sess.ID, "COD-9", "tracker")
	if err != nil || !found {
		t.Fatalf("set issue: found=%v err=%v", found, err)
	}
	if updated.IssueID != "COD-9" || updated.IssueDestination != "tracker" {
		t.Fatalf("anchored issue = %q dest %q, want COD-9 in tracker", updated.IssueID, updated.IssueDestination)
	}

	got, _, err := g.Session(sess.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got.IssueID != "COD-9" || got.IssueDestination != "tracker" {
		t.Fatalf("persisted issue = %q dest %q, want COD-9 in tracker", got.IssueID, got.IssueDestination)
	}

	if _, _, err := g.SetIssue(sess.ID, "ACME-1", "internal"); err != nil {
		t.Fatalf("re-anchor: %v", err)
	}
	if got, _, err := g.Session(sess.ID); err != nil || got.IssueID != "ACME-1" || got.IssueDestination != "internal" {
		t.Fatalf("re-anchored issue = %q dest %q (err=%v), want ACME-1 in internal", got.IssueID, got.IssueDestination, err)
	}

	if _, found, err := g.SetIssue(9999, "COD-1", "tracker"); found || err != nil {
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

	all, err := g.List("acme", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 || all[0].ID != b.ID || all[1].ID != a.ID {
		t.Fatalf("list newest-first = %+v", all)
	}

	if _, err := g.Transition(b.ID, GrillWaiting, ""); err != nil {
		t.Fatalf("transition: %v", err)
	}
	waiting, err := g.List("acme", GrillWaiting, "")
	if err != nil {
		t.Fatalf("list waiting: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != b.ID {
		t.Fatalf("list waiting = %+v, want [%d]", waiting, b.ID)
	}
}

func TestGrillListModeFilter(t *testing.T) {
	g, _ := testGrill(t, 0)
	legacy, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	interview, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-2", Mode: GrillModeInterview})
	research, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-3", Mode: GrillModeResearch})

	for _, tc := range []struct {
		mode string
		want []int64
	}{
		{mode: "", want: []int64{research.ID, interview.ID, legacy.ID}},
		{mode: GrillModeInterview, want: []int64{interview.ID, legacy.ID}},
		{mode: GrillModeResearch, want: []int64{research.ID}},
	} {
		sessions, err := g.List("acme", "", tc.mode)
		if err != nil {
			t.Fatalf("list mode %q: %v", tc.mode, err)
		}
		got := make([]int64, len(sessions))
		for i, sess := range sessions {
			got[i] = sess.ID
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("list mode %q = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestGrillSetMode(t *testing.T) {
	g, _ := testGrill(t, 0)
	sess, _ := g.Create(NewGrillSession{Repo: "acme"})

	stamped, found, err := g.SetMode(sess.ID, GrillModeResearch)
	if err != nil || !found {
		t.Fatalf("set mode: found=%v err=%v", found, err)
	}
	if stamped.Mode != GrillModeResearch {
		t.Fatalf("stamped mode = %q, want research", stamped.Mode)
	}
	if got, _, _ := g.Session(sess.ID); got.Mode != GrillModeResearch {
		t.Fatalf("stored mode = %q, want research", got.Mode)
	}
	if _, found, err := g.SetMode(9999, GrillModeResearch); found || err != nil {
		t.Fatalf("set mode on unknown session = (found=%v, err=%v), want (false, nil)", found, err)
	}
}

func TestGrillListAwaitingAcrossRepos(t *testing.T) {
	g, _ := testGrill(t, 0)
	waiting, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	parked, _ := g.Create(NewGrillSession{Repo: "other", IssueID: "COD-2"})
	stalled, _ := g.Create(NewGrillSession{Repo: "other", IssueID: "COD-3"})
	finished, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-4"})
	running, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-5"})

	for _, move := range []struct {
		id    int64
		state string
	}{
		{waiting.ID, GrillWaiting},
		{parked.ID, GrillParked},
		{stalled.ID, GrillStalled},
		{finished.ID, GrillFinished},
	} {
		if _, err := g.Transition(move.id, move.state, ""); err != nil {
			t.Fatalf("transition %d to %s: %v", move.id, move.state, err)
		}
	}

	awaiting, err := g.ListAwaiting()
	if err != nil {
		t.Fatalf("list awaiting: %v", err)
	}
	got := map[int64]bool{}
	for _, sess := range awaiting {
		got[sess.ID] = true
	}
	if len(awaiting) != 3 || !got[waiting.ID] || !got[parked.ID] || !got[stalled.ID] {
		t.Fatalf("awaiting = %+v, want the waiting, parked and stalled sessions", awaiting)
	}
	if got[finished.ID] || got[running.ID] {
		t.Fatalf("awaiting = %+v, want no finished or running session", awaiting)
	}
}

// seedGrillIssues writes the issue rows a session joins against, so a test can close
// an issue out from under its session.
func seedGrillIssues(t *testing.T, db *sql.DB, repo string, groups map[string]string) {
	t.Helper()
	issues := make([]Issue, 0, len(groups))
	for id, group := range groups {
		issues = append(issues, Issue{Identifier: id, StatusGroup: group})
	}
	if _, _, err := NewIssues(db).Upsert(repo, "linear", issues); err != nil {
		t.Fatalf("seed issues: %v", err)
	}
}

// blockedGrillSession opens a session on issueID and parks it in a blocked state.
func blockedGrillSession(t *testing.T, g *Grill, repo, issueID, state string) GrillSession {
	t.Helper()
	sess, err := g.Create(NewGrillSession{Repo: repo, IssueID: issueID})
	if err != nil {
		t.Fatalf("create session on %q: %v", issueID, err)
	}
	moved, err := g.Transition(sess.ID, state, "")
	if err != nil {
		t.Fatalf("transition %d to %s: %v", sess.ID, state, err)
	}
	return moved
}

func TestGrillListAwaitingExcludesClosedIssue(t *testing.T) {
	g, db := testGrill(t, 0)
	seedGrillIssues(t, db, "acme", map[string]string{
		"COD-1": "started",
		"COD-2": "started",
		"COD-3": "started",
		"COD-4": "started",
	})
	waiting := blockedGrillSession(t, g, "acme", "COD-1", GrillWaiting)
	parked := blockedGrillSession(t, g, "acme", "COD-2", GrillParked)
	stalled := blockedGrillSession(t, g, "acme", "COD-3", GrillStalled)
	open := blockedGrillSession(t, g, "acme", "COD-4", GrillWaiting)
	draft := blockedGrillSession(t, g, "acme", "", GrillWaiting)

	awaiting, err := g.ListAwaiting()
	if err != nil {
		t.Fatalf("list awaiting: %v", err)
	}
	if len(awaiting) != 5 {
		t.Fatalf("awaiting = %+v, want all five while their issues are open", awaiting)
	}

	seedGrillIssues(t, db, "acme", map[string]string{
		"COD-1": "done",
		"COD-2": "canceled",
		"COD-3": "done",
	})
	awaiting, err = g.ListAwaiting()
	if err != nil {
		t.Fatalf("list awaiting after close: %v", err)
	}
	got := map[int64]bool{}
	for _, sess := range awaiting {
		got[sess.ID] = true
	}
	if len(awaiting) != 2 || !got[open.ID] || !got[draft.ID] {
		t.Fatalf("awaiting = %+v, want the open-issue session and the draft", awaiting)
	}
	for _, closed := range []GrillSession{waiting, parked, stalled} {
		if stored, _, _ := g.Session(closed.ID); stored.State != closed.State {
			t.Fatalf("session %d state = %q, want %q — the read path never writes", closed.ID, stored.State, closed.State)
		}
	}
}

func TestGrillListRunning(t *testing.T) {
	g, db := testGrill(t, 0)
	seedGrillIssues(t, db, "acme", map[string]string{
		"COD-1": "started",
		"COD-2": "started",
		"COD-3": "done",
	})

	cases := []struct {
		name  string
		repo  string
		issue string
		mode  string
		state string
		want  bool
	}{
		{name: "interview mid-turn", repo: "acme", issue: "COD-1", mode: GrillModeInterview, want: true},
		{name: "research mid-turn", repo: "other", issue: "OTH-1", mode: GrillModeResearch, want: true},
		{name: "legacy row with no mode", repo: "acme", issue: "COD-2", want: true},
		{name: "draft with no issue", repo: "acme", want: true},
		{name: "waiting on the user", repo: "acme", issue: "OTH-2", state: GrillWaiting},
		{name: "parked", repo: "acme", issue: "OTH-3", state: GrillParked},
		{name: "stalled", repo: "other", issue: "OTH-4", state: GrillStalled},
		{name: "finished", repo: "other", issue: "OTH-5", state: GrillFinished},
		{name: "running on a closed issue", repo: "acme", issue: "COD-3"},
	}

	ids := make([]int64, len(cases))
	for i, tc := range cases {
		sess, err := g.Create(NewGrillSession{Repo: tc.repo, IssueID: tc.issue, Mode: tc.mode})
		if err != nil {
			t.Fatalf("create %s: %v", tc.name, err)
		}
		if tc.state != "" {
			if _, err := g.Transition(sess.ID, tc.state, ""); err != nil {
				t.Fatalf("transition %s to %s: %v", tc.name, tc.state, err)
			}
		}
		ids[i] = sess.ID
	}

	running, err := g.ListRunning()
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	got := map[int64]bool{}
	for _, sess := range running {
		got[sess.ID] = true
	}
	for i, tc := range cases {
		if got[ids[i]] != tc.want {
			t.Errorf("%s listed = %v, want %v", tc.name, got[ids[i]], tc.want)
		}
	}
}

func TestGrillListExcludesClosedIssue(t *testing.T) {
	g, db := testGrill(t, 0)
	seedGrillIssues(t, db, "acme", map[string]string{"COD-1": "done", "COD-2": "started", "COD-3": "done"})
	running, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-1"})
	open, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-2"})
	draft, _ := g.Create(NewGrillSession{Repo: "acme"})
	settled := blockedGrillSession(t, g, "acme", "COD-3", GrillFinished)
	if _, err := g.Transition(settled.ID, GrillApplied, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}

	list, err := g.List("acme", "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[int64]bool{}
	for _, sess := range list {
		got[sess.ID] = true
	}
	if len(list) != 3 || !got[open.ID] || !got[draft.ID] || !got[settled.ID] {
		t.Fatalf("list = %+v, want the open-issue session, the draft and the applied session", list)
	}
	if got[running.ID] {
		t.Fatalf("list = %+v, want no session on the closed issue", list)
	}
}

func TestGrillRetireClosed(t *testing.T) {
	g, db := testGrill(t, 0)
	seedGrillIssues(t, db, "acme", map[string]string{
		"COD-1": "done",
		"COD-2": "canceled",
		"COD-3": "done",
		"COD-4": "done",
		"COD-5": "started",
		"COD-6": "done",
	})
	waiting := blockedGrillSession(t, g, "acme", "COD-1", GrillWaiting)
	parked := blockedGrillSession(t, g, "acme", "COD-2", GrillParked)
	stalled := blockedGrillSession(t, g, "acme", "COD-3", GrillStalled)
	finished := blockedGrillSession(t, g, "acme", "COD-4", GrillFinished)
	open := blockedGrillSession(t, g, "acme", "COD-5", GrillWaiting)
	draft := blockedGrillSession(t, g, "acme", "", GrillParked)
	running, _ := g.Create(NewGrillSession{Repo: "acme", IssueID: "COD-6"})

	retired, err := g.RetireClosed("acme")
	if err != nil {
		t.Fatalf("retire closed: %v", err)
	}
	wantReasons := map[int64]string{
		waiting.ID:  "retired: COD-1 closed",
		parked.ID:   "retired: COD-2 closed",
		stalled.ID:  "retired: COD-3 closed",
		finished.ID: "retired: COD-4 closed",
	}
	if len(retired) != len(wantReasons) {
		t.Fatalf("retired = %+v, want %d sessions", retired, len(wantReasons))
	}
	for _, sess := range retired {
		if sess.State != GrillAbandoned {
			t.Fatalf("retired %d state = %q, want abandoned", sess.ID, sess.State)
		}
		if sess.ParkedReason != wantReasons[sess.ID] {
			t.Fatalf("retired %d reason = %q, want %q", sess.ID, sess.ParkedReason, wantReasons[sess.ID])
		}
		stored, _, _ := g.Session(sess.ID)
		if stored.State != GrillAbandoned || stored.ParkedReason != wantReasons[sess.ID] {
			t.Fatalf("stored %d = %q/%q, want abandoned with the reason", sess.ID, stored.State, stored.ParkedReason)
		}
	}

	for _, kept := range []struct {
		sess  GrillSession
		state string
	}{
		{open, GrillWaiting},
		{draft, GrillParked},
		{running, GrillRunning},
	} {
		stored, _, _ := g.Session(kept.sess.ID)
		if stored.State != kept.state {
			t.Fatalf("session %d state = %q, want %q", kept.sess.ID, stored.State, kept.state)
		}
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

	list, err := g.List("acme", "", "")
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

func TestGrillAuthoringTitleFromSeed(t *testing.T) {
	g, _ := testGrill(t, 0)
	authoring, _ := g.Create(NewGrillSession{Repo: "acme"})
	if _, _, err := g.AppendMessage(authoring.ID, NewGrillMessage{
		Role:    GrillRoleUser,
		Kind:    GrillKindInfo,
		Payload: `{"text":"Add a dark-mode toggle"}`,
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	sess, found, err := g.Session(authoring.ID)
	if err != nil || !found {
		t.Fatalf("session(%d) = %v, %v", authoring.ID, found, err)
	}
	if sess.IssueTitle != "Add a dark-mode toggle" {
		t.Fatalf("authoring title = %q, want the seed", sess.IssueTitle)
	}
}

func TestGrillReportTitleFromResearchOutcome(t *testing.T) {
	g, _ := testGrill(t, 0)
	research, _ := g.Create(NewGrillSession{Repo: "acme", Mode: GrillModeResearch})
	authored, _ := g.Create(NewGrillSession{Repo: "acme"})
	for _, seed := range []struct {
		id      int64
		payload string
	}{
		{research.ID, `{"disposition":"research","title":"How the SDK retries","findings":"# Report"}`},
		{research.ID, `{"disposition":"research","title":"How the SDK retries, revised","findings":"# Report"}`},
		{authored.ID, `{"disposition":"create","title":"Add a dark-mode toggle"}`},
	} {
		if _, _, err := g.AppendMessage(seed.id, NewGrillMessage{
			Role:    GrillRoleAgent,
			Kind:    GrillKindOutcome,
			Payload: seed.payload,
		}); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
	}

	sess, found, err := g.Session(research.ID)
	if err != nil || !found {
		t.Fatalf("session(%d) = %v, %v", research.ID, found, err)
	}
	if sess.ReportTitle != "How the SDK retries, revised" {
		t.Fatalf("report title = %q, want the latest outcome's", sess.ReportTitle)
	}

	// A create's proposed title names an issue, not a report, so it stays off the row.
	other, _, err := g.Session(authored.ID)
	if err != nil {
		t.Fatalf("session(%d): %v", authored.ID, err)
	}
	if other.ReportTitle != "" {
		t.Fatalf("create report title = %q, want empty", other.ReportTitle)
	}
}

func TestGrillSetTitleOutranksOutcome(t *testing.T) {
	g, _ := testGrill(t, 0)
	research, _ := g.Create(NewGrillSession{Repo: "acme", Mode: GrillModeResearch})
	if _, _, err := g.AppendMessage(research.ID, NewGrillMessage{
		Role:    GrillRoleAgent,
		Kind:    GrillKindOutcome,
		Payload: `{"disposition":"research","title":"How the SDK retries","findings":"# Report"}`,
	}); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}

	renamed, found, err := g.SetTitle(research.ID, "SDK retry behaviour")
	if err != nil || !found {
		t.Fatalf("set title: found=%v err=%v", found, err)
	}
	if renamed.ReportTitle != "SDK retry behaviour" {
		t.Fatalf("returned title = %q, want the override", renamed.ReportTitle)
	}

	// A follow-up turn proposing its own title must not take the name back.
	if _, _, err := g.AppendMessage(research.ID, NewGrillMessage{
		Role:    GrillRoleAgent,
		Kind:    GrillKindOutcome,
		Payload: `{"disposition":"research","title":"How the SDK retries, revised","findings":"# Report"}`,
	}); err != nil {
		t.Fatalf("seed later outcome: %v", err)
	}
	got, found, err := g.Session(research.ID)
	if err != nil || !found {
		t.Fatalf("session(%d) = %v, %v", research.ID, found, err)
	}
	if got.ReportTitle != "SDK retry behaviour" {
		t.Fatalf("report title = %q, want the override", got.ReportTitle)
	}

	if _, found, err := g.SetTitle(999, "nothing"); found || err != nil {
		t.Fatalf("set title on unknown session: found=%v err=%v, want false nil", found, err)
	}
}

func TestGrillDeleteRemovesSessionAndMessages(t *testing.T) {
	g, db := testGrill(t, 0)
	sess, _ := g.Create(NewGrillSession{Repo: "acme", Mode: GrillModeResearch})
	if _, _, err := g.AppendMessage(sess.ID, NewGrillMessage{
		Role:    GrillRoleAgent,
		Kind:    GrillKindOutcome,
		Payload: `{"disposition":"research","title":"How the SDK retries","findings":"# Report"}`,
	}); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}

	deleted, err := g.Delete(sess.ID)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, found, err := g.Session(sess.ID); found || err != nil {
		t.Fatalf("session after delete: found=%v err=%v, want false nil", found, err)
	}
	var messages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM grill_messages WHERE session_id = ?`, sess.ID).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("messages after delete = %d, want 0", messages)
	}

	if deleted, err := g.Delete(sess.ID); deleted || err != nil {
		t.Fatalf("delete again: deleted=%v err=%v, want false nil", deleted, err)
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
	remaining, err := g.List("acme", "", "")
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

func TestGrillPruneKeepsAppliedResearchBeyondWindow(t *testing.T) {
	g, db := testGrill(t, 2)

	settle := func(mode, state string) GrillSession {
		t.Helper()
		s, err := g.Create(NewGrillSession{Repo: "acme", Mode: mode})
		if err != nil {
			t.Fatalf("create %s: %v", mode, err)
		}
		if _, err := db.Exec(`UPDATE grill_sessions SET state = ? WHERE id = ?`, state, s.ID); err != nil {
			t.Fatalf("settle %d as %s: %v", s.ID, state, err)
		}
		return s
	}

	report := settle(GrillModeResearch, GrillApplied)
	discarded := settle(GrillModeResearch, GrillAbandoned)
	interview := settle(GrillModeInterview, GrillApplied)
	for i := 0; i < 2; i++ {
		if _, err := g.Create(NewGrillSession{Repo: "acme"}); err != nil {
			t.Fatalf("create filler %d: %v", i, err)
		}
	}
	if _, _, err := g.AppendMessage(report.ID, NewGrillMessage{
		Role:    GrillRoleAgent,
		Kind:    GrillKindOutcome,
		Payload: `{"report":"findings"}`,
	}); err != nil {
		t.Fatalf("append report: %v", err)
	}

	if err := g.Prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if _, found, err := g.Session(report.ID); err != nil || !found {
		t.Fatalf("applied research session pruned: found=%v err=%v", found, err)
	}
	msgs, err := g.Messages(report.ID, 0)
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Payload != `{"report":"findings"}` {
		t.Fatalf("messages = %+v, want the surviving report", msgs)
	}
	for _, gone := range []GrillSession{discarded, interview} {
		if _, found, err := g.Session(gone.ID); err != nil || found {
			t.Fatalf("session %d survived prune: found=%v err=%v", gone.ID, found, err)
		}
	}
}

func TestGrillRetireClosedIgnoresAge(t *testing.T) {
	g, db := testGrill(t, 0)
	seedGrillIssues(t, db, "acme", map[string]string{"COD-1": "started"})
	open := blockedGrillSession(t, g, "acme", "COD-1", GrillParked)
	draft := blockedGrillSession(t, g, "acme", "", GrillParked)

	old := formatGrillTime(time.Now().Add(-365 * 24 * time.Hour))
	if _, err := db.Exec(`UPDATE grill_sessions SET updated_at = ?`, old); err != nil {
		t.Fatalf("backdate sessions: %v", err)
	}

	retired, err := g.RetireClosed("acme")
	if err != nil {
		t.Fatalf("retire closed: %v", err)
	}
	if len(retired) != 0 {
		t.Fatalf("retired = %+v, want none — age alone never retires a session", retired)
	}
	for _, sess := range []GrillSession{open, draft} {
		if got, _, _ := g.Session(sess.ID); got.State != GrillParked {
			t.Fatalf("session %d state = %q, want parked", sess.ID, got.State)
		}
	}
}
