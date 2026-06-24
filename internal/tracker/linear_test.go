package tracker

import "testing"

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
