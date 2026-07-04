package planning

import "testing"

func TestParseValid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Status
	}{
		{
			name: "prd",
			raw:  `{"status":"prd","prd":{"title":"T","markdown":"# hi"}}`,
			want: StatusPRD,
		},
		{
			name: "prd in code fence",
			raw:  "```json\n{\"status\":\"prd\",\"prd\":{\"title\":\"T\",\"markdown\":\"# hi\"}}\n```",
			want: StatusPRD,
		},
		{
			name: "questions",
			raw:  `{"status":"questions","questions":[{"id":"q1","text":"why?","options":[{"label":"a"}]}]}`,
			want: StatusQuestions,
		},
		{
			name: "free-text question needs no options",
			raw:  `{"status":"questions","questions":[{"id":"q1","text":"name it?","kind":"text"}]}`,
			want: StatusQuestions,
		},
		{
			name: "multi-select question",
			raw:  `{"status":"questions","questions":[{"id":"q1","text":"which?","kind":"multi","options":[{"label":"a"},{"label":"b"}]}]}`,
			want: StatusQuestions,
		},
		{
			name: "slices",
			raw:  `{"status":"slices","slices":[{"title":"first slice"}]}`,
			want: StatusSlices,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse(%q) errored: %v", tc.name, err)
			}
			if p.Status != tc.want {
				t.Errorf("status = %q, want %q", p.Status, tc.want)
			}
		})
	}
}

func TestResolvedKind(t *testing.T) {
	cases := []struct {
		name string
		q    Question
		want QuestionKind
	}{
		{"explicit multi wins", Question{Kind: KindMulti, Options: []Option{{Label: "a"}}}, KindMulti},
		{"options infer single", Question{Options: []Option{{Label: "a"}}}, KindSingle},
		{"no options infer text", Question{}, KindText},
		{"unknown kind falls back to inference", Question{Kind: "bogus", Options: []Option{{Label: "a"}}}, KindSingle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.ResolvedKind(); got != tc.want {
				t.Errorf("ResolvedKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"malformed json", `{"status":"prd",`},
		{"no json object", "sorry, I could not do this"},
		{"empty", ""},
		{"unknown status", `{"status":"draft","prd":{"markdown":"x"}}`},
		{"missing status", `{"prd":{"markdown":"x"}}`},
		{"prd missing markdown", `{"status":"prd","prd":{"title":"T"}}`},
		{"prd missing object", `{"status":"prd"}`},
		{"questions empty", `{"status":"questions","questions":[]}`},
		{"question missing text", `{"status":"questions","questions":[{"id":"q1"}]}`},
		{"option-less single select", `{"status":"questions","questions":[{"id":"q1","text":"pick?","kind":"single"}]}`},
		{"option-less multi select", `{"status":"questions","questions":[{"id":"q1","text":"pick?","kind":"multi"}]}`},
		{"slices empty", `{"status":"slices","slices":[]}`},
		{"slice missing title", `{"status":"slices","slices":[{"description":"x"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.raw); err == nil {
				t.Errorf("Parse(%q) = nil error, want an error", tc.raw)
			}
		})
	}
}
