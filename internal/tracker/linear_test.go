package tracker

import (
	"context"
	"fmt"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
)

func TestParseIssueStatus(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    IssueStatus
		matched bool
	}{
		{"done sentinel", "STATUS=done", StatusDone, true},
		{"completed synonym", "STATUS=completed", StatusDone, true},
		{"closed synonym", "STATUS=closed", StatusDone, true},
		{"canceled sentinel", "STATUS=canceled", StatusCanceled, true},
		{"cancelled spelling", "STATUS=cancelled", StatusCanceled, true},
		{"open sentinel", "STATUS=open", StatusOpen, true},
		{"in-progress synonym", "STATUS=in-progress", StatusOpen, true},
		{"case-insensitive key and value", "status=Done", StatusDone, true},
		{"line prefix before sentinel", "The issue COD-566 STATUS=done", StatusDone, true},
		{"last sentinel wins", "STATUS=open\nSTATUS=done", StatusDone, true},
		{"no sentinel", "I could not find the issue", StatusUnknown, false},
		{"unrecognized value", "STATUS=frobnicated", StatusUnknown, false},
	}
	for _, tc := range tests {
		got, ok := parseIssueStatus(tc.text)
		if got != tc.want || ok != tc.matched {
			t.Errorf("%s: parseIssueStatus(%q) = (%q, %v), want (%q, %v)", tc.name, tc.text, got, ok, tc.want, tc.matched)
		}
	}
}

func TestMapLinearState(t *testing.T) {
	tests := []struct {
		stateType string
		want      IssueStatus
	}{
		{"completed", StatusDone},
		{"canceled", StatusCanceled},
		{"backlog", StatusOpen},
		{"unstarted", StatusOpen},
		{"started", StatusOpen},
		{"", StatusOpen},
	}
	for _, tc := range tests {
		if got := mapLinearState(tc.stateType); got != tc.want {
			t.Errorf("mapLinearState(%q) = %q, want %q", tc.stateType, got, tc.want)
		}
	}
}

func TestIssueStatusTerminal(t *testing.T) {
	for _, s := range []IssueStatus{StatusDone, StatusCanceled} {
		if !s.Terminal() {
			t.Errorf("%q.Terminal() = false, want true", s)
		}
	}
	for _, s := range []IssueStatus{StatusOpen, StatusUnknown} {
		if s.Terminal() {
			t.Errorf("%q.Terminal() = true, want false", s)
		}
	}
}

func TestInProject(t *testing.T) {
	tests := []struct {
		candidate, scope string
		want             bool
	}{
		{"trau", "", true},                // no scope → everything matches
		{"trau", "trau", true},            // exact
		{"salonradar.com", "trau", false}, // mismatch
		{"  trau  ", "trau", true},        // trims
		{"Trau", "trau", true},            // case-insensitive
		{"", "trau", false},               // candidate has no project
	}
	for _, tc := range tests {
		if got := inProject(tc.candidate, tc.scope); got != tc.want {
			t.Errorf("inProject(%q, %q) = %v, want %v", tc.candidate, tc.scope, got, tc.want)
		}
	}
}

func TestScopeProjectClause(t *testing.T) {
	if c := (Scope{}).projectClause(); c != "" {
		t.Errorf("empty project should yield no clause, got %q", c)
	}
	c := Scope{Project: "trau"}.projectClause()
	if c == "" || !contains(c, "trau") {
		t.Errorf("projectClause should mention the project, got %q", c)
	}
}

func TestParseProject(t *testing.T) {
	tests := []struct {
		text        string
		wantName    string
		wantMatched bool
	}{
		{"PROJECT=trau", "trau", true},
		{"blah\nPROJECT=salonradar.com", "salonradar.com", true},
		{"PROJECT=NONE", "", true},
		{"PROJECT=none", "", true},
		{"no sentinel here", "", false},
	}
	for _, tc := range tests {
		name, matched := parseProject(tc.text)
		if name != tc.wantName || matched != tc.wantMatched {
			t.Errorf("parseProject(%q) = (%q,%v), want (%q,%v)", tc.text, name, matched, tc.wantName, tc.wantMatched)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
func TestParseSubIssuesReadsHasChildren(t *testing.T) {
	subs, ok := parseSubIssuesJSON(`SUB_ISSUES=[{"id":"COD-1","title":"leaf","hasChildren":false},{"id":"COD-2","title":"epic","hasChildren":true}]`)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-issues, got %d", len(subs))
	}
	if subs[0].ID != "COD-1" || subs[0].HasChildren {
		t.Errorf("first sub-issue should be a leaf, got %+v", subs[0])
	}
	if subs[1].ID != "COD-2" || !subs[1].HasChildren {
		t.Errorf("second sub-issue should be a nested epic, got %+v", subs[1])
	}
}

func TestPickParentScopeSkipsNestedEpics(t *testing.T) {
	runner := &recordingRunner{}
	l := &Linear{
		Runner:     runner,
		ReadyLabel: "ready-for-agent",
		Team:       "COD",
	}

	// First call lists sub-issues; second call is the pick. The agent tries to
	// return the nested epic COD-500, but the hard leaf filter should reject it
	// and return nothing eligible.
	runner.responses = map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"COD-500","title":"nested epic","hasChildren":true},{"id":"COD-501","title":"leaf","hasChildren":false}]`},
		"pick":       {Final: `PICK=COD-500`},
	}

	id, err := l.Pick(context.Background(), Scope{Parent: "COD-493", Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if id != "" {
		t.Fatalf("expected no eligible ticket when agent picks a nested epic, got %s", id)
	}
	if runner.calls["sub_issues"] != 1 || runner.calls["pick"] != 1 {
		t.Fatalf("expected one sub_issues and one pick call, got %v", runner.calls)
	}
}

func TestPickParentScopeReturnsLeaf(t *testing.T) {
	runner := &recordingRunner{}
	l := &Linear{
		Runner:     runner,
		ReadyLabel: "ready-for-agent",
		Team:       "COD",
	}

	runner.responses = map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"COD-500","title":"nested epic","hasChildren":true},{"id":"COD-501","title":"leaf","hasChildren":false}]`},
		"pick":       {Final: `PICK=COD-501`},
	}

	id, err := l.Pick(context.Background(), Scope{Parent: "COD-493", Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if id != "COD-501" {
		t.Fatalf("expected COD-501, got %s", id)
	}
}

func TestPickParentScopeNoLeaves(t *testing.T) {
	runner := &recordingRunner{}
	l := &Linear{
		Runner:     runner,
		ReadyLabel: "ready-for-agent",
		Team:       "COD",
	}

	runner.responses = map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"COD-500","title":"nested epic","hasChildren":true}]`},
	}

	id, err := l.Pick(context.Background(), Scope{Parent: "COD-493", Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if id != "" {
		t.Fatalf("expected no eligible ticket when all sub-issues are nested epics, got %s", id)
	}
	if runner.calls["pick"] != 0 {
		t.Fatalf("expected no pick call when there are no leaves, got %d", runner.calls["pick"])
	}
}

type recordingRunner struct {
	responses map[string]agent.Result
	calls     map[string]int
}

func (r *recordingRunner) Run(_ context.Context, _ string, label string) (agent.Result, error) {
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[label]++
	res, ok := r.responses[label]
	if !ok {
		return agent.Result{}, fmt.Errorf("no fake response for label %q", label)
	}
	return res, nil
}

func TestParseParent(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    string
		matched bool
	}{
		{"plain sentinel", "PARENT=M4C-54", "M4C-54", true},
		{"none means no parent", "PARENT=NONE", "", true},
		{"case-insensitive none value", "PARENT=none", "", true},
		{"lowercase key not matched", "parent=M4C-54", "", false},
		{"empty value not matched", "PARENT=", "", false},
		{"line prefix before sentinel", "The issue M4C-57 PARENT=M4C-54", "M4C-54", true},
		{"last sentinel wins", "PARENT=M4C-10\nPARENT=M4C-54", "M4C-54", true},
		{"no sentinel", "I could not find a parent", "", false},
	}
	for _, tc := range tests {
		got, ok := parseParent(tc.text)
		if got != tc.want || ok != tc.matched {
			t.Errorf("%s: parseParent(%q) = (%q, %v), want (%q, %v)", tc.name, tc.text, got, ok, tc.want, tc.matched)
		}
	}
}

// With no API key the direct path is unavailable, so ParentIssue must fall back to
// the MCP runner and parse its PARENT= sentinel.
func TestParentIssueFallsBackToRunner(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"parent": {Final: "PARENT=M4C-54"},
	}}
	l := &Linear{Runner: runner, Team: "M4C"}

	got, err := l.ParentIssue(context.Background(), "M4C-57")
	if err != nil {
		t.Fatalf("ParentIssue returned error: %v", err)
	}
	if got != "M4C-54" {
		t.Errorf("ParentIssue = %q, want %q", got, "M4C-54")
	}
	if runner.calls["parent"] != 1 {
		t.Errorf("expected exactly one parent lookup, got %d", runner.calls["parent"])
	}
}

func TestParentIssueTopLevelReturnsEmpty(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"parent": {Final: "PARENT=NONE"},
	}}
	l := &Linear{Runner: runner, Team: "M4C"}

	got, err := l.ParentIssue(context.Background(), "M4C-1")
	if err != nil {
		t.Fatalf("ParentIssue returned error: %v", err)
	}
	if got != "" {
		t.Errorf("ParentIssue = %q, want empty (top-level issue)", got)
	}
}

func TestParseSubIssuesReadsDone(t *testing.T) {
	subs, ok := parseSubIssuesJSON(`SUB_ISSUES=[{"id":"COD-1","title":"open","hasChildren":false,"done":false},{"id":"COD-2","title":"shipped","hasChildren":false,"done":true}]`)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-issues, got %d", len(subs))
	}
	if subs[0].Done {
		t.Errorf("first sub-issue should be open, got %+v", subs[0])
	}
	if !subs[1].Done {
		t.Errorf("second sub-issue should be done, got %+v", subs[1])
	}
}

// TestPickParentScopeExcludesDoneLeaves is the COD-708 regression guard: a leaf
// the tracker already considers finished must never survive the leaf filter,
// even when the pick agent returns it — a merged ticket that gets re-picked
// re-enters build and faults on its merge-deleted branch.
func TestPickParentScopeExcludesDoneLeaves(t *testing.T) {
	tests := []struct {
		name string
		pick string
		want string
	}{
		{name: "agent picks the done leaf", pick: "PICK=COD-681", want: ""},
		{name: "agent picks the open leaf", pick: "PICK=COD-682", want: "COD-682"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{responses: map[string]agent.Result{
				"sub_issues": {Final: `SUB_ISSUES=[{"id":"COD-681","title":"shipped","hasChildren":false,"done":true},{"id":"COD-682","title":"open","hasChildren":false,"done":false}]`},
				"pick":       {Final: tt.pick},
			}}
			l := &Linear{Runner: runner, ReadyLabel: "ready-for-agent", Team: "COD"}

			id, err := l.Pick(context.Background(), Scope{Parent: "COD-530", Team: "COD", Prefix: "COD"})
			if err != nil {
				t.Fatalf("Pick returned error: %v", err)
			}
			if id != tt.want {
				t.Fatalf("Pick = %q, want %q", id, tt.want)
			}
		})
	}
}
