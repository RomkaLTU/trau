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
