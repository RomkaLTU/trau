package pipeline

import "testing"

// TestParseOpenPRURL is the COD-750 regression guard: gh's branch lookup falls
// back to the most recent merged/closed PR when the branch has no open one, so
// PRURL must filter on state — adopting a merged PR made CommitAndPR skip
// opening a fresh PR for rebuilt work and CIAndMerge reconcile it straight to
// Done with the redo commits stranded on the branch.
func TestParseOpenPRURL(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"open PR", `{"url":"https://github.com/o/r/pull/8","state":"OPEN"}`, "https://github.com/o/r/pull/8"},
		{"merged PR is not adopted", `{"url":"https://github.com/o/r/pull/6","state":"MERGED"}`, ""},
		{"closed PR is not adopted", `{"url":"https://github.com/o/r/pull/7","state":"CLOSED"}`, ""},
		{"empty output", ``, ""},
		{"malformed JSON", `no pull requests found for branch`, ""},
		{"missing state", `{"url":"https://github.com/o/r/pull/9"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOpenPRURL(tc.out); got != tc.want {
				t.Fatalf("parseOpenPRURL(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}
