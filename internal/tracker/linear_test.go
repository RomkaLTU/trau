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
