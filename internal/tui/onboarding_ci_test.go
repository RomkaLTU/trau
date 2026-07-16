package tui

import (
	"strings"
	"testing"
)

func TestCIDescription(t *testing.T) {
	cases := []struct {
		name    string
		det     CIDetection
		wantSub string
	}{
		{
			name:    "branch protection with checks",
			det:     CIDetection{Gate: true, Confident: true, Source: "branch-protection", ExpectedChecks: []string{"build", "test"}},
			wantSub: "PRs require: build, test",
		},
		{
			name:    "branch protection no named checks",
			det:     CIDetection{Gate: true, Confident: true, Source: "branch-protection"},
			wantSub: "requires PR status checks",
		},
		{
			name:    "local workflow fallback",
			det:     CIDetection{Gate: true, Source: "workflows"},
			wantSub: ".github/workflows",
		},
		{
			name:    "all workflows path-filtered",
			det:     CIDetection{Gate: true, Source: "workflows", PathFiltered: true},
			wantSub: "path-filtered",
		},
		{
			name:    "nothing detected",
			det:     CIDetection{Gate: false, Source: "none"},
			wantSub: "No pull_request-triggered workflow found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := onboardingModel{ciDet: tc.det}
			got := m.ciDescription()
			if !strings.Contains(got, tc.wantSub) {
				t.Fatalf("ciDescription() = %q, want substring %q", got, tc.wantSub)
			}
		})
	}
}

func TestExpectedChecksLabelCaps(t *testing.T) {
	d := CIDetection{ExpectedChecks: []string{"a", "b", "c", "d", "e"}}
	got := d.expectedChecksLabel()
	if !strings.Contains(got, "a, b, c") || !strings.Contains(got, "+2 more") {
		t.Fatalf("expectedChecksLabel() = %q, want capped list with '+2 more'", got)
	}
	short := CIDetection{ExpectedChecks: []string{"only"}}
	if got := short.expectedChecksLabel(); got != "only" {
		t.Fatalf("expectedChecksLabel() = %q, want %q", got, "only")
	}
}
