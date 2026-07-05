package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestSubIssueState(t *testing.T) {
	cases := []struct {
		name string
		sub  tracker.SubIssue
		want string
	}{
		{"open leaf is todo", tracker.SubIssue{ID: "COD-1"}, "todo"},
		{"finished child is done", tracker.SubIssue{ID: "COD-2", Done: true}, "done"},
		{"nested parent is epic", tracker.SubIssue{ID: "COD-3", HasChildren: true}, "epic"},
		{"done wins over nested", tracker.SubIssue{ID: "COD-4", Done: true, HasChildren: true}, "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := subIssueState(c.sub); got != c.want {
				t.Errorf("subIssueState(%+v) = %q, want %q", c.sub, got, c.want)
			}
		})
	}
}

func TestWriteEpicSubIssuesJSON(t *testing.T) {
	var buf bytes.Buffer
	subs := []tracker.SubIssue{
		{ID: "COD-1", Title: "First", Done: true},
		{ID: "COD-2", Title: "Second"},
		{ID: "COD-3", Title: "Nested", HasChildren: true},
	}
	if err := writeEpicSubIssuesJSON(&buf, subs); err != nil {
		t.Fatalf("writeEpicSubIssuesJSON: %v", err)
	}

	var got []epicSubIssue
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, buf.String())
	}
	if len(got) != 3 {
		t.Fatalf("decoded %d sub-issues, want 3", len(got))
	}
	if got[0].ID != "COD-1" || got[0].Title != "First" || got[0].State != "done" {
		t.Errorf("sub[0] = %+v, want COD-1/First/done", got[0])
	}
	if got[1].State != "todo" {
		t.Errorf("sub[1].State = %q, want todo", got[1].State)
	}
	if got[2].State != "epic" {
		t.Errorf("sub[2].State = %q, want epic", got[2].State)
	}
}

func TestWriteEpicSubIssuesJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeEpicSubIssuesJSON(&buf, nil); err != nil {
		t.Fatalf("writeEpicSubIssuesJSON(nil): %v", err)
	}
	if got := bytes.TrimSpace(buf.Bytes()); string(got) != "[]" {
		t.Errorf("childless epic = %q, want []", got)
	}
}
