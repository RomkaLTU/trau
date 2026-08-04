package pipeline

import "testing"

// TestParseOpenPRURL is the COD-750 regression guard: gh's branch lookup falls
// back to the most recent merged/closed PR when the branch has no open one, so
// PRURL must filter on state — adopting a merged PR made CommitAndPR skip
// opening a fresh PR for rebuilt work and CIAndMerge reconcile it straight to
// Done with the redo commits stranded on the branch.
func TestParseOpenPRURL(t *testing.T) {
	cases := []struct {
		name      string
		out       string
		want      string
		wantDraft bool
	}{
		{"open PR", `{"url":"https://github.com/o/r/pull/8","state":"OPEN"}`, "https://github.com/o/r/pull/8", false},
		{"draft PR is open but not ready", `{"url":"https://github.com/o/r/pull/5","state":"OPEN","isDraft":true}`, "https://github.com/o/r/pull/5", true},
		{"merged PR is not adopted", `{"url":"https://github.com/o/r/pull/6","state":"MERGED"}`, "", false},
		{"closed PR is not adopted", `{"url":"https://github.com/o/r/pull/7","state":"CLOSED"}`, "", false},
		{"empty output", ``, "", false},
		{"malformed JSON", `no pull requests found for branch`, "", false},
		{"missing state", `{"url":"https://github.com/o/r/pull/9"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, draft := parseOpenPRURL(tc.out)
			if got != tc.want || draft != tc.wantDraft {
				t.Fatalf("parseOpenPRURL(%q) = %q, %v, want %q, %v", tc.out, got, draft, tc.want, tc.wantDraft)
			}
		})
	}
}
