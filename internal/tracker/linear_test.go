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
