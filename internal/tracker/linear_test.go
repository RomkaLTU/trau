package tracker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// richPickPayload and minimalPickPayload are the two real pick result-file
// payloads captured from the M4C-54 incident (see the COD ticket): a rich JSON
// object with a reason and a nested candidates map, and the minimal one-field
// form. The rich payload used to defeat parsePickJSON (it unmarshalled into
// map[string]*string, which the nested candidates object breaks), silently
// yielding "0 tickets" even though the agent picked correctly.
const richPickPayload = `{
  "pick": "M4C-105",
  "reason": "Qualifying issues (ready-for-agent + not-started + all blockers Done): M4C-105, M4C-106, M4C-108 — all Medium priority, no due date. M4C-107 excluded (no 'ready-for-agent' label). Tie broken by lowest issue number.",
  "candidates": {
    "M4C-105": {"label_ready_for_agent": true, "stateType": "unstarted", "blockedBy": [], "priority": "Medium", "dueDate": null, "qualifies": true},
    "M4C-106": {"label_ready_for_agent": true, "stateType": "unstarted", "blockedBy": [], "priority": "Medium", "dueDate": null, "qualifies": true},
    "M4C-107": {"label_ready_for_agent": false, "stateType": "backlog", "blockedBy": [], "priority": "Low", "dueDate": null, "qualifies": false},
    "M4C-108": {"label_ready_for_agent": true, "stateType": "backlog", "blockedBy": [], "priority": "Medium", "dueDate": null, "qualifies": true}
  }
}`

const minimalPickPayload = `{"pick": "M4C-105"}`

func TestParsePick(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		prefix  string
		want    string
		matched bool
	}{
		{"rich JSON payload", richPickPayload, "M4C", "M4C-105", true},
		{"minimal JSON payload", minimalPickPayload, "M4C", "M4C-105", true},
		{"issue key variant", `{"issue": "M4C-42"}`, "M4C", "M4C-42", true},
		{"JSON none", `{"pick": "NONE"}`, "M4C", "", true},
		{"JSON empty string is nothing eligible", `{"pick": ""}`, "M4C", "", true},
		{"JSON null is nothing eligible", `{"pick": null}`, "M4C", "", true},
		{"JSON lowercase id accepted", `{"pick": "m4c-105"}`, "M4C", "m4c-105", true},
		{"JSON with surrounding noise still parses via sentinel", "here you go\nPICK=M4C-9", "M4C", "M4C-9", true},
		{"sentinel plain", "PICK=COD-414", "COD", "COD-414", true},
		{"sentinel none", "PICK=NONE", "COD", "", true},
		{"sentinel with line prefix", "The winner is PICK=COD-7 today", "COD", "COD-7", true},
		{"sentinel last wins", "PICK=COD-1\nPICK=COD-2", "COD", "COD-2", true},
		{"no sentinel, no json", "I could not decide which ticket to pick", "COD", "", false},
		{"json without pick or issue key", `{"selection": "COD-1"}`, "COD", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := parsePick(tc.text, tc.prefix)
			if got != tc.want || matched != tc.matched {
				t.Errorf("parsePick(%q, %q) = (%q, %v), want (%q, %v)", tc.text, tc.prefix, got, matched, tc.want, tc.matched)
			}
		})
	}
}

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
		{"started sentinel", "STATUS=started", StatusStarted, true},
		{"in-progress synonym", "STATUS=in-progress", StatusStarted, true},
		{"backlog synonym", "STATUS=backlog", StatusOpen, true},
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
		{"started", StatusStarted},
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
	for _, s := range []IssueStatus{StatusOpen, StatusStarted, StatusUnknown} {
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
	prompts   map[string]string
}

func (r *recordingRunner) Run(_ context.Context, prompt string, label string) (agent.Result, error) {
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	if r.prompts == nil {
		r.prompts = make(map[string]string)
	}
	r.calls[label]++
	r.prompts[label] = prompt
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

// TestPickEpicMCPMatchesLeafCaseInsensitively covers the MCP fallback guard: an
// agent that answers with a lowercase identifier (or the rich JSON payload from
// the M4C-54 incident) must still resolve to the canonical leaf, not be dropped.
func TestPickEpicMCPMatchesLeafCaseInsensitively(t *testing.T) {
	tests := []struct {
		name string
		pick string
		want string
	}{
		{"lowercase JSON id", `{"pick":"m4c-105"}`, "M4C-105"},
		{"rich JSON payload", richPickPayload, "M4C-105"},
		{"minimal JSON payload", minimalPickPayload, "M4C-105"},
		{"uppercase sentinel", "PICK=M4C-105", "M4C-105"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{responses: map[string]agent.Result{
				"sub_issues": {Final: `SUB_ISSUES=[{"id":"M4C-105","title":"leaf","hasChildren":false,"done":false}]`},
				"pick":       {Final: tt.pick},
			}}
			l := &Linear{Runner: runner, ReadyLabel: "ready-for-agent", Team: "M4C"}

			id, err := l.Pick(context.Background(), Scope{Parent: "M4C-54", Team: "M4C", Prefix: "M4C"})
			if err != nil {
				t.Fatalf("Pick returned error: %v", err)
			}
			if id != tt.want {
				t.Fatalf("Pick = %q, want %q (canonical leaf)", id, tt.want)
			}
		})
	}
}

// TestPickEpicMCPUnparseableSurfacesError is the core of the incident fix: when
// the runner succeeds but its output carries no recoverable pick, Pick must
// surface an explicit error rather than "" — a silent 0-ticket session is
// indistinguishable from a genuinely empty queue and hides ready work.
func TestPickEpicMCPUnparseableSurfacesError(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"M4C-105","title":"leaf","hasChildren":false,"done":false}]`},
		"pick":       {Final: "I looked but could not decide on a ticket."},
	}}
	l := &Linear{Runner: runner, ReadyLabel: "ready-for-agent", Team: "M4C"}

	id, err := l.Pick(context.Background(), Scope{Parent: "M4C-54", Team: "M4C", Prefix: "M4C"})
	if err == nil {
		t.Fatalf("expected an explicit parse error, got id=%q err=nil", id)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty on parse failure", id)
	}
	if !strings.Contains(err.Error(), "could not parse pick") {
		t.Fatalf("err = %v, want it to mention 'could not parse pick'", err)
	}
}

// TestPickEpicMCPNoneWhileLeavesRemain: an agent NONE answer while the confirmed
// leaf set is non-empty is a legitimate "everything left is blocked" result, so
// Pick returns "" with no error (the discrepancy is only logged to stderr).
func TestPickEpicMCPNoneWhileLeavesRemain(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"sub_issues": {Final: `SUB_ISSUES=[{"id":"M4C-105","title":"leaf","hasChildren":false,"done":false}]`},
		"pick":       {Final: `{"pick":"NONE"}`},
	}}
	l := &Linear{Runner: runner, ReadyLabel: "ready-for-agent", Team: "M4C"}

	id, err := l.Pick(context.Background(), Scope{Parent: "M4C-54", Team: "M4C", Prefix: "M4C"})
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty on NONE", id)
	}
}

// TestPickTeamMCPUnparseableSurfacesError is the team-queue analogue: an
// unparseable MCP pick surfaces an explicit error, never a silent empty queue.
func TestPickTeamMCPUnparseableSurfacesError(t *testing.T) {
	runner := &recordingRunner{responses: map[string]agent.Result{
		"pick": {Final: "no clear sentinel here"},
	}}
	l := &Linear{Runner: runner, ReadyLabel: "ready-for-agent", Team: "COD"}

	id, err := l.Pick(context.Background(), Scope{Team: "COD", Prefix: "COD"})
	if err == nil || id != "" {
		t.Fatalf("Pick = (%q, %v), want (\"\", non-nil error)", id, err)
	}
	if !strings.Contains(err.Error(), "could not parse pick") {
		t.Fatalf("err = %v, want 'could not parse pick'", err)
	}
}

// linCand builds one PickIssues node for the fake Linear server. blockerStates
// lists the workflow-state type of each "blocked by" issue (empty = unblocked);
// childCount>0 marks the node a non-leaf epic container.
func linCand(id string, priority int, blockerStates []string, childCount int) string {
	blockers := make([]string, 0, len(blockerStates))
	for i, st := range blockerStates {
		blockers = append(blockers, fmt.Sprintf(`{"type":"blocks","issue":{"id":"blk-%d","identifier":"BLK-%d","state":{"type":%q}}}`, i, i, st))
	}
	kids := make([]string, 0, childCount)
	for i := 0; i < childCount; i++ {
		kids = append(kids, fmt.Sprintf(`{"id":"kid-%d"}`, i))
	}
	return fmt.Sprintf(`{"id":"iss-%s","identifier":%q,"priority":%d,"state":{"type":"unstarted"},"project":{"name":""},"labels":{"nodes":[{"id":"l","name":"ready-for-agent"}]},"children":{"nodes":[%s]},"inverseRelations":{"nodes":[%s]}}`,
		id, id, priority, strings.Join(kids, ","), strings.Join(blockers, ","))
}

// linChild builds one Issue-query child node for a parent's leaf enumeration.
func linChild(id string) string {
	return fmt.Sprintf(`{"id":"c-%s","identifier":%q,"state":{"type":"unstarted"},"children":{"nodes":[]}}`, id, id)
}

// fakeLinearPick returns a Linear wired to a fake GraphQL endpoint that answers
// the Teams, Issue (parent children) and PickIssues queries from the given node
// fragments, so the deterministic API pick path can be exercised without network.
func fakeLinearPick(t *testing.T, childrenNodes, pickNodes string) *Linear {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "query Teams"):
			_, _ = io.WriteString(w, `{"data":{"teams":{"nodes":[{"id":"team-1","key":"COD","name":"Codesome"}]}}}`)
		case strings.Contains(q, "query Issue"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[{"id":"epic-1","identifier":"COD-530","team":{"id":"team-1","key":"COD"},"children":{"nodes":[`+childrenNodes+`]}}]}}}`)
		case strings.Contains(q, "query PickIssues"):
			_, _ = io.WriteString(w, `{"data":{"issues":{"nodes":[`+pickNodes+`]}}}`)
		default:
			t.Errorf("unexpected GraphQL query: %s", q)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return &Linear{ReadyLabel: "ready-for-agent", Team: "COD", APIKey: "lin_key", endpoint: srv.URL}
}

// TestPickEpicAPIEnforcesBlockerOrder verifies the deterministic epic pick makes
// the blocker gate and run order code-enforced: a leaf blocked by an open sibling
// is invisible, ties fall to the lowest number only among unblocked peers, a
// canceled blocker no longer blocks, and the pick follows the dependency chain as
// blockers finish. No pick agent is ever consulted.
func TestPickEpicAPIEnforcesBlockerOrder(t *testing.T) {
	// Both COD-701 and COD-702 are confirmed leaves of the epic.
	children := linChild("COD-701") + "," + linChild("COD-702")

	tests := []struct {
		name  string
		picks string
		want  string
	}{
		{
			name:  "blocked lower number is skipped for the free peer",
			picks: linCand("COD-701", 3, []string{"unstarted"}, 0) + "," + linCand("COD-702", 3, nil, 0),
			want:  "COD-702",
		},
		{
			name:  "once the blocker completes the chain advances to COD-701",
			picks: linCand("COD-701", 3, []string{"completed"}, 0) + "," + linCand("COD-702", 3, nil, 0),
			want:  "COD-701",
		},
		{
			name:  "a canceled blocker no longer blocks",
			picks: linCand("COD-701", 3, []string{"canceled"}, 0) + "," + linCand("COD-702", 3, []string{"unstarted"}, 0),
			want:  "COD-701",
		},
		{
			name:  "priority outranks number, number only tie-breaks unblocked peers",
			picks: linCand("COD-701", 3, nil, 0) + "," + linCand("COD-702", 2, nil, 0),
			want:  "COD-702", // higher priority (2=high) beats the lower-numbered medium
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := fakeLinearPick(t, children, tt.picks)
			id, err := l.Pick(context.Background(), Scope{Parent: "COD-530", Team: "COD", Prefix: "COD"})
			if err != nil {
				t.Fatalf("Pick error: %v", err)
			}
			if id != tt.want {
				t.Fatalf("Pick = %q, want %q", id, tt.want)
			}
		})
	}
}

// TestPickEpicAPINeverPicksNonLeafOrOutOfSet: a ready candidate that is not in the
// epic's confirmed leaf set — an unrelated leaf or a nested epic — is never
// returned even when it sorts first, because the deterministic path intersects
// candidates with the leaf set.
func TestPickEpicAPINeverPicksNonLeafOrOutOfSet(t *testing.T) {
	children := linChild("COD-701") // only COD-701 is a leaf of this epic
	// COD-700 sorts first (urgent) but is a nested epic and not in the leaf set;
	// COD-900 is an unrelated ready leaf, also not in this epic.
	picks := linCand("COD-700", 1, nil, 2) + "," + linCand("COD-900", 1, nil, 0) + "," + linCand("COD-701", 3, nil, 0)

	l := fakeLinearPick(t, children, picks)
	id, err := l.Pick(context.Background(), Scope{Parent: "COD-530", Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if id != "COD-701" {
		t.Fatalf("Pick = %q, want COD-701 (out-of-set and nested-epic candidates skipped)", id)
	}
}

// TestPickTeamAPISkipsEpicAndBlocked exercises the team-queue deterministic path:
// an epic container and a blocked ticket are skipped in run order, and the first
// eligible leaf is returned — without consulting the pick agent.
func TestPickTeamAPISkipsEpicAndBlocked(t *testing.T) {
	// COD-700 urgent epic (skip), COD-701 high but blocked (skip), COD-702 medium leaf (pick).
	picks := linCand("COD-700", 1, nil, 2) + "," + linCand("COD-701", 2, []string{"started"}, 0) + "," + linCand("COD-702", 3, nil, 0)
	l := fakeLinearPick(t, "", picks)

	id, err := l.Pick(context.Background(), Scope{Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("Pick error: %v", err)
	}
	if id != "COD-702" {
		t.Fatalf("Pick = %q, want COD-702 (epic + blocked skipped)", id)
	}
}

func TestAllBlockersCompleted(t *testing.T) {
	ref := func(stateType string) linearapi.IssueRef {
		return linearapi.IssueRef{State: linearapi.State{Type: stateType}}
	}
	tests := []struct {
		name string
		refs []linearapi.IssueRef
		want bool
	}{
		{"no blockers", nil, true},
		{"single completed", []linearapi.IssueRef{ref("completed")}, true},
		{"single canceled unblocks", []linearapi.IssueRef{ref("canceled")}, true},
		{"completed and canceled", []linearapi.IssueRef{ref("completed"), ref("canceled")}, true},
		{"one still unstarted blocks", []linearapi.IssueRef{ref("completed"), ref("unstarted")}, false},
		{"started blocks", []linearapi.IssueRef{ref("started")}, false},
		{"backlog blocks", []linearapi.IssueRef{ref("backlog")}, false},
	}
	for _, tc := range tests {
		if got := allBlockersCompleted(tc.refs); got != tc.want {
			t.Errorf("%s: allBlockersCompleted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSelectEligibleLeaf(t *testing.T) {
	cand := func(id string, children int, blocked bool, project string) linearapi.PickCandidate {
		c := linearapi.PickCandidate{Issue: linearapi.Issue{
			Identifier: id,
			State:      linearapi.State{Type: "unstarted"},
			Project:    linearapi.Project{Name: project},
		}}
		for i := 0; i < children; i++ {
			c.Children = append(c.Children, linearapi.IssueRef{ID: fmt.Sprintf("k%d", i)})
		}
		if blocked {
			c.BlockedBy = []linearapi.IssueRef{{State: linearapi.State{Type: "unstarted"}}}
		}
		return c
	}

	leaves := map[string]bool{"COD-2": true, "COD-3": true}
	candidates := []linearapi.PickCandidate{
		cand("COD-1", 2, false, ""), // epic, and not in leaf set
		cand("COD-2", 0, true, ""),  // leaf but blocked
		cand("COD-3", 0, false, ""), // eligible leaf
	}

	// Epic scope: only COD-3 survives the intersection + gate.
	if got := selectEligibleLeaf(candidates, "COD", "", leaves); got != "COD-3" {
		t.Errorf("epic selectEligibleLeaf = %q, want COD-3", got)
	}
	// Team scope (nil leaves): COD-1 skipped as an epic, COD-2 skipped as blocked,
	// COD-3 chosen.
	if got := selectEligibleLeaf(candidates, "COD", "", nil); got != "COD-3" {
		t.Errorf("team selectEligibleLeaf = %q, want COD-3", got)
	}
	// A scoped project filters out a candidate in a different project.
	scoped := []linearapi.PickCandidate{cand("COD-3", 0, false, "other")}
	if got := selectEligibleLeaf(scoped, "COD", "trau", nil); got != "" {
		t.Errorf("project-scoped selectEligibleLeaf = %q, want empty (wrong project)", got)
	}
}

// linCandHier builds one PickIssues node carrying an explicit parent and child
// count, so the eligible listing's hierarchy threading can be asserted. parentJSON
// is the raw "parent" value (an object or "null"); childCount>0 marks an epic.
func linCandHier(id, parentJSON string, childCount int) string {
	kids := make([]string, 0, childCount)
	for i := 0; i < childCount; i++ {
		kids = append(kids, fmt.Sprintf(`{"id":"kid-%d"}`, i))
	}
	return fmt.Sprintf(`{"id":"iss-%s","identifier":%q,"priority":3,"state":{"type":"unstarted"},"project":{"name":""},"parent":%s,"labels":{"nodes":[{"id":"l","name":"ready-for-agent"}]},"children":{"nodes":[%s]},"inverseRelations":{"nodes":[]}}`,
		id, id, parentJSON, strings.Join(kids, ","))
}

// TestListEligibleAPIThreadsHierarchy verifies the deterministic eligible listing
// carries epic hierarchy: a sub-issue reports its parent epic, a top-level ticket
// reports an empty parent, and a ready-labelled epic reports has-children.
func TestListEligibleAPIThreadsHierarchy(t *testing.T) {
	picks := strings.Join([]string{
		linCandHier("COD-806", `{"id":"epic-1","identifier":"COD-805"}`, 0),
		linCandHier("COD-810", `null`, 0),
		linCandHier("COD-805", `null`, 2),
	}, ",")
	l := fakeLinearPick(t, "", picks)

	got, err := l.ListEligible(context.Background(), Scope{Team: "COD", Prefix: "COD"})
	if err != nil {
		t.Fatalf("ListEligible error: %v", err)
	}
	byID := make(map[string]ListedTicket, len(got))
	for _, tk := range got {
		byID[tk.ID] = tk
	}

	if sub := byID["COD-806"]; sub.Parent != "COD-805" || sub.HasChildren {
		t.Errorf("sub-issue = %+v, want Parent COD-805 and HasChildren false", sub)
	}
	if top := byID["COD-810"]; top.Parent != "" || top.HasChildren {
		t.Errorf("top-level = %+v, want empty Parent and HasChildren false", top)
	}
	if epic := byID["COD-805"]; !epic.HasChildren || epic.Parent != "" {
		t.Errorf("epic = %+v, want HasChildren true and empty Parent", epic)
	}
}

// TestParseEligibleParentOptional confirms the MCP fallback parser reads a parent
// when present and stays additive: output without a parent still parses, leaving
// the field empty rather than failing.
func TestParseEligibleParentOptional(t *testing.T) {
	withParent := `ELIGIBLE=[{"id":"COD-806","title":"Sub","parent":"COD-805","labels":["ready-for-agent"]}]`
	list, ok := parseEligible(withParent)
	if !ok || len(list) != 1 {
		t.Fatalf("parseEligible(withParent) = (%v, %v), want one ticket", list, ok)
	}
	if list[0].Parent != "COD-805" {
		t.Errorf("Parent = %q, want COD-805", list[0].Parent)
	}

	oldShape := `ELIGIBLE=[{"id":"COD-810","title":"Top","labels":["ready-for-agent"]}]`
	list, ok = parseEligible(oldShape)
	if !ok || len(list) != 1 {
		t.Fatalf("parseEligible(oldShape) = (%v, %v), want one ticket", list, ok)
	}
	if list[0].Parent != "" {
		t.Errorf("Parent = %q, want empty for old-shape output", list[0].Parent)
	}
}
