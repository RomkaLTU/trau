package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// grillApplyServer builds a server with a registered repo and an injected fake
// writer, returning the repo root so a test can seed the synced issue store.
func grillApplyServer(t *testing.T, fake tracker.Writer) (*httptest.Server, *hubstore.Stores, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	stores := testStoresAt(t, home)
	root := filepath.Join(t.TempDir(), "acme")
	repo := registry.Repo{Name: "acme", Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("remember repo: %v", err)
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, stores)
	s.home = home
	s.newWriter = func(config.Config) (tracker.Writer, error) { return fake, nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, stores, root
}

// seedFinishedGrill opens a session, records a Q&A and the finish_session outcome,
// and settles it to finished — the state the review UI applies from.
func seedFinishedGrill(t *testing.T, stores *hubstore.Stores, root, issueID string, outcome grillOutcome) int64 {
	t.Helper()
	sess, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: root, IssueID: issueID})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	addMsg := func(role, kind, payload string) {
		if _, _, err := stores.Grill().AppendMessage(sess.ID, hubstore.NewGrillMessage{Role: role, Kind: kind, Payload: payload}); err != nil {
			t.Fatalf("append %s: %v", kind, err)
		}
	}
	addMsg(hubstore.GrillRoleAgent, hubstore.GrillKindQuestion, `{"text":"which flow?"}`)
	addMsg(hubstore.GrillRoleUser, hubstore.GrillKindAnswer, `{"text":"the checkout flow"}`)
	body, _ := json.Marshal(outcome)
	addMsg(hubstore.GrillRoleAgent, hubstore.GrillKindOutcome, string(body))
	if _, err := stores.Grill().Transition(sess.ID, hubstore.GrillFinished, ""); err != nil {
		t.Fatalf("finish session: %v", err)
	}
	return sess.ID
}

func applyGrill(t *testing.T, ts *httptest.Server, sid int64, req GrillApplyRequest) (*http.Response, GrillApplyResponse) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/grill/"+strconv.FormatInt(sid, 10)+"/apply", req)
	var out GrillApplyResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode apply: %v", err)
		}
	}
	_ = res.Body.Close()
	return res, out
}

func stepStatus(steps []GrillApplyStep, name string) (string, bool) {
	for _, s := range steps {
		if s.Step == name {
			return s.Status, true
		}
	}
	return "", false
}

func TestGrillApplyRewriteOrdering(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "A crisp new description.",
		Summary:             "clarified the flow",
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", res.StatusCode)
	}
	if !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("apply result = %+v, want applied", out)
	}
	if want := []string{"description", "comment", "labels"}; !slices.Equal(fake.order, want) {
		t.Fatalf("call order = %v, want %v", fake.order, want)
	}
	for _, step := range out.Steps {
		if step.Status != grillStepOK {
			t.Fatalf("step %s = %+v, want ok", step.Step, step)
		}
	}
	if len(fake.descriptions) != 1 || fake.descriptions[0].body != "A crisp new description." {
		t.Fatalf("descriptions = %+v", fake.descriptions)
	}
	if len(fake.comments) != 1 || fake.comments[0].id != "COD-1" {
		t.Fatalf("comments = %+v", fake.comments)
	}
}

func TestGrillApplyUserEditedDescription(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "agent draft",
		Summary:             "done",
	})

	if _, out := applyGrill(t, ts, sid, GrillApplyRequest{ProposedDescription: "user edited body"}); !out.Applied {
		t.Fatalf("apply result = %+v, want applied", out)
	}
	if len(fake.descriptions) != 1 || fake.descriptions[0].body != "user edited body" {
		t.Fatalf("descriptions = %+v, want the user-edited body", fake.descriptions)
	}
}

func TestGrillApplyPerStepFailureAndRetry(t *testing.T) {
	fake := newFakeWriter()
	fake.commentErr = errString("linear: 502")
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "new body",
		Summary:             "done",
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", res.StatusCode)
	}
	if out.Applied || out.Session.State != hubstore.GrillFinished {
		t.Fatalf("partial apply = %+v, want not applied and still finished", out)
	}
	if st, _ := stepStatus(out.Steps, "description"); st != grillStepOK {
		t.Errorf("description step = %q, want ok", st)
	}
	if st, _ := stepStatus(out.Steps, "comment"); st != grillStepFailed {
		t.Errorf("comment step = %q, want failed", st)
	}
	if st, _ := stepStatus(out.Steps, "labels"); st != grillStepOK {
		t.Errorf("labels step = %q, want ok (later steps still attempted)", st)
	}

	// The tracker recovered; re-apply settles the session.
	fake.commentErr = nil
	_, out = applyGrill(t, ts, sid, GrillApplyRequest{})
	if !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("re-apply = %+v, want applied", out)
	}
}

func TestGrillApplyLabelMatrix(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		subIssues   []grillSubIssue
		createQueue []fakeCreate
		wantDesc    bool
		wantAdd     []string
		wantRemove  []string
		wantWrites  bool
	}{
		{"rewrite", grillDispRewrite, nil, nil, true, []string{"ready-for-agent"}, []string{"needs-triage", "needs-info"}, true},
		{
			"split", grillDispSplit,
			[]grillSubIssue{{Title: "Slice A", Description: "do a"}},
			[]fakeCreate{{issue: tracker.NewIssue{Identifier: "COD-2"}}},
			true, nil, []string{"needs-triage", "needs-info", "needs-split"}, true,
		},
		{"needs_split", grillDispNeedsSplit, nil, nil, false, []string{"needs-split"}, []string{"needs-triage", "needs-info"}, true},
		{"no_change", grillDispNoChange, nil, nil, false, nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeWriter()
			fake.createQueue = tc.createQueue
			ts, stores, root := grillApplyServer(t, fake)
			sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
				Disposition:         tc.disposition,
				ProposedDescription: "body",
				SubIssues:           tc.subIssues,
				Summary:             "done",
			})

			_, out := applyGrill(t, ts, sid, GrillApplyRequest{})
			if !out.Applied {
				t.Fatalf("apply = %+v, want applied", out)
			}
			if !tc.wantWrites {
				if len(fake.order) != 0 {
					t.Fatalf("no_change wrote to the tracker: %v", fake.order)
				}
				if len(out.Steps) != 0 {
					t.Fatalf("no_change steps = %+v, want none", out.Steps)
				}
				return
			}
			if got := len(fake.descriptions) > 0; got != tc.wantDesc {
				t.Errorf("described = %v, want %v", got, tc.wantDesc)
			}
			if len(fake.labels) != 1 {
				t.Fatalf("label calls = %d, want 1", len(fake.labels))
			}
			if !slices.Equal(fake.labels[0].add, tc.wantAdd) {
				t.Errorf("add = %v, want %v", fake.labels[0].add, tc.wantAdd)
			}
			if !slices.Equal(fake.labels[0].remove, tc.wantRemove) {
				t.Errorf("remove = %v, want %v", fake.labels[0].remove, tc.wantRemove)
			}
		})
	}
}

func TestGrillApplySyncNoClobber(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)
	if _, _, err := stores.Issues().Upsert(root, "linear", []hubstore.Issue{{
		Identifier:  "COD-1",
		Title:       "Unclear ticket",
		Description: "stale one-liner",
		Status:      "Triage",
		StatusGroup: "backlog",
		Labels:      []string{"needs-triage"},
	}}); err != nil {
		t.Fatalf("seed synced issue: %v", err)
	}
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "fully specified body",
		Summary:             "clarified",
	})

	if _, out := applyGrill(t, ts, sid, GrillApplyRequest{}); !out.Applied {
		t.Fatalf("apply = %+v, want applied", out)
	}

	iss, found, err := stores.Issues().Get(root, "COD-1")
	if err != nil || !found {
		t.Fatalf("get issue: found=%v err=%v", found, err)
	}
	if iss.Description != "fully specified body" {
		t.Errorf("stored description = %q, want the applied body", iss.Description)
	}
	if hasLabel(iss.Labels, "needs-triage") {
		t.Errorf("stored labels still carry needs-triage: %v", iss.Labels)
	}
	if !hasLabel(iss.Labels, "ready-for-agent") {
		t.Errorf("stored labels missing ready-for-agent: %v", iss.Labels)
	}
}

func TestGrillApplySplitHierarchyAndRelations(t *testing.T) {
	fake := newFakeWriter()
	fake.createQueue = []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-2"}},
		{issue: tracker.NewIssue{Identifier: "COD-3"}},
		{issue: tracker.NewIssue{Identifier: "COD-4"}},
	}
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic goal framing.",
		Summary:             "sliced into three",
		SubIssues: []grillSubIssue{
			{Title: "Slice A", Description: "do a"},
			{Title: "Slice B", Description: "do b", BlockedBy: []int{0}},
			{Title: "Slice C", Description: "do c", Labels: []string{"custom"}, BlockedBy: []int{0, 1}},
		},
	})

	res, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if res.StatusCode != http.StatusOK || !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("apply = %+v (status %d), want applied", out, res.StatusCode)
	}
	if len(fake.descriptions) != 1 || fake.descriptions[0].id != "COD-1" || fake.descriptions[0].body != "Epic goal framing." {
		t.Fatalf("parent description = %+v", fake.descriptions)
	}
	if len(fake.created) != 3 {
		t.Fatalf("created = %d, want 3 children", len(fake.created))
	}
	for i, d := range fake.created {
		if d.Parent != "COD-1" {
			t.Errorf("sub-issue %d parent = %q, want COD-1", i, d.Parent)
		}
	}
	if got := fake.created[0].Labels; !slices.Equal(got, []string{"ready-for-agent"}) {
		t.Errorf("slice A labels = %v, want the default ready label", got)
	}
	if got := fake.created[2].Labels; !slices.Equal(got, []string{"custom"}) {
		t.Errorf("slice C labels = %v, want its proposed labels", got)
	}
	// Blocking relations: blocker first. B←A, C←A, C←B.
	wantLinks := []linkCall{
		{blocker: "COD-2", blocked: "COD-3"},
		{blocker: "COD-2", blocked: "COD-4"},
		{blocker: "COD-3", blocked: "COD-4"},
	}
	if !slices.Equal(fake.links, wantLinks) {
		t.Fatalf("links = %+v, want %+v", fake.links, wantLinks)
	}
	if len(fake.labels) != 1 {
		t.Fatalf("label calls = %d, want 1 (parent only)", len(fake.labels))
	}
	if fake.labels[0].add != nil {
		t.Errorf("parent add labels = %v, want none", fake.labels[0].add)
	}
	if want := []string{"needs-triage", "needs-info", "needs-split"}; !slices.Equal(fake.labels[0].remove, want) {
		t.Errorf("parent remove labels = %v, want %v", fake.labels[0].remove, want)
	}
	wantSteps := []string{
		"description", "sub-issue: Slice A", "sub-issue: Slice B", "sub-issue: Slice C",
		"relations", "comment", "labels",
	}
	gotSteps := make([]string, 0, len(out.Steps))
	for _, s := range out.Steps {
		gotSteps = append(gotSteps, s.Step)
		if s.Status != grillStepOK {
			t.Errorf("step %s = %+v, want ok", s.Step, s)
		}
	}
	if !slices.Equal(gotSteps, wantSteps) {
		t.Fatalf("steps = %v, want %v", gotSteps, wantSteps)
	}
	kids, err := stores.Issues().Children(root, "COD-1")
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(kids) != 3 {
		t.Fatalf("stored children = %d, want 3 (clobber guard)", len(kids))
	}
}

func TestGrillApplySplitPartialRetry(t *testing.T) {
	fake := newFakeWriter()
	// Pass 1: three creates land, the last two fail.
	fake.createQueue = []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-2"}},
		{issue: tracker.NewIssue{Identifier: "COD-3"}},
		{issue: tracker.NewIssue{Identifier: "COD-4"}},
		{err: errString("linear: 502")},
		{err: errString("linear: 502")},
	}
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic goal.",
		Summary:             "five slices",
		SubIssues: []grillSubIssue{
			{Title: "S1", Description: "d1"},
			{Title: "S2", Description: "d2", BlockedBy: []int{0}},
			{Title: "S3", Description: "d3"},
			{Title: "S4", Description: "d4", BlockedBy: []int{2}},
			{Title: "S5", Description: "d5", BlockedBy: []int{3}},
		},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if out.Applied || out.Session.State != hubstore.GrillFinished {
		t.Fatalf("partial apply = %+v, want not applied and still finished", out)
	}
	if len(fake.created) != 5 {
		t.Fatalf("pass 1 create attempts = %d, want 5", len(fake.created))
	}
	if st, _ := stepStatus(out.Steps, "sub-issue: S4"); st != grillStepFailed {
		t.Errorf("S4 step = %q, want failed", st)
	}

	// Pass 2: only the two missing slices are created; the three already filed are reused.
	fake.created = nil
	fake.createIdx = 0
	fake.createQueue = []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-5"}},
		{issue: tracker.NewIssue{Identifier: "COD-6"}},
	}
	fake.links = nil

	_, out = applyGrill(t, ts, sid, GrillApplyRequest{})
	if !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("retry = %+v, want applied", out)
	}
	if len(fake.created) != 2 {
		t.Fatalf("retry create attempts = %d, want 2 (only the missing slices, no duplicates)", len(fake.created))
	}
	if titles := []string{fake.created[0].Title, fake.created[1].Title}; !slices.Equal(titles, []string{"S4", "S5"}) {
		t.Fatalf("retry created = %v, want [S4 S5]", titles)
	}
	// The freshly created slices get their relations wired now; the reused ones are not re-linked.
	wantLinks := []linkCall{
		{blocker: "COD-4", blocked: "COD-5"},
		{blocker: "COD-5", blocked: "COD-6"},
	}
	if !slices.Equal(fake.links, wantLinks) {
		t.Fatalf("retry links = %+v, want %+v", fake.links, wantLinks)
	}
	kids, err := stores.Issues().Children(root, "COD-1")
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(kids) != 5 {
		t.Fatalf("stored children after retry = %d, want 5", len(kids))
	}
}

func TestGrillApplySplitRelationRetry(t *testing.T) {
	fake := newFakeWriter()
	// Pass 1: both slices are created, but the link between them fails.
	fake.createQueue = []fakeCreate{
		{issue: tracker.NewIssue{Identifier: "COD-2"}},
		{issue: tracker.NewIssue{Identifier: "COD-3"}},
	}
	fake.linkErr = errString("linear: 502")
	ts, stores, root := grillApplyServer(t, fake)
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispSplit,
		ProposedDescription: "Epic goal.",
		Summary:             "two slices",
		SubIssues: []grillSubIssue{
			{Title: "S1", Description: "d1"},
			{Title: "S2", Description: "d2", BlockedBy: []int{0}},
		},
	})

	_, out := applyGrill(t, ts, sid, GrillApplyRequest{})
	if out.Applied || out.Session.State != hubstore.GrillFinished {
		t.Fatalf("partial apply = %+v, want not applied and still finished", out)
	}
	if st, ok := stepStatus(out.Steps, "relations"); !ok || st != grillStepFailed {
		t.Errorf("relations step = %q (present %v), want failed", st, ok)
	}
	if want := []linkCall{{blocker: "COD-2", blocked: "COD-3"}}; !slices.Equal(fake.links, want) {
		t.Fatalf("pass 1 links = %+v, want %+v", fake.links, want)
	}

	// Pass 2: both slices already exist, so nothing is re-created; the link that
	// failed before is re-attempted and now lands.
	fake.linkErr = nil
	fake.created = nil
	fake.createIdx = 0
	fake.createQueue = nil
	fake.links = nil

	_, out = applyGrill(t, ts, sid, GrillApplyRequest{})
	if !out.Applied || out.Session.State != hubstore.GrillApplied {
		t.Fatalf("retry = %+v, want applied", out)
	}
	if len(fake.created) != 0 {
		t.Fatalf("retry created %d issues, want 0 (both slices reused)", len(fake.created))
	}
	if want := []linkCall{{blocker: "COD-2", blocked: "COD-3"}}; !slices.Equal(fake.links, want) {
		t.Fatalf("retry links = %+v, want the failed relation re-attempted %+v", fake.links, want)
	}
	if st, ok := stepStatus(out.Steps, "relations"); !ok || st != grillStepOK {
		t.Errorf("retry relations step = %q (present %v), want ok", st, ok)
	}
}

func TestGrillApplyStateGuards(t *testing.T) {
	fake := newFakeWriter()
	ts, stores, root := grillApplyServer(t, fake)

	// A running session has no proposed outcome to apply.
	running, err := stores.Grill().Create(hubstore.NewGrillSession{Repo: root, IssueID: "COD-2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res, _ := applyGrill(t, ts, running.ID, GrillApplyRequest{}); res.StatusCode != http.StatusConflict {
		t.Fatalf("apply running status = %d, want 409", res.StatusCode)
	}

	// Applying a finished session twice: the second attempt is refused.
	sid := seedFinishedGrill(t, stores, root, "COD-1", grillOutcome{
		Disposition:         grillDispRewrite,
		ProposedDescription: "body",
		Summary:             "done",
	})
	if _, out := applyGrill(t, ts, sid, GrillApplyRequest{}); !out.Applied {
		t.Fatalf("first apply = %+v, want applied", out)
	}
	if res, _ := applyGrill(t, ts, sid, GrillApplyRequest{}); res.StatusCode != http.StatusConflict {
		t.Fatalf("re-apply of applied status = %d, want 409", res.StatusCode)
	}
}
