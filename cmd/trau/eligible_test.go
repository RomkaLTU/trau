package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestWriteEligibleJSON(t *testing.T) {
	var buf bytes.Buffer
	tickets := []tracker.ListedTicket{
		{ID: "COD-1", Title: "First", State: "Todo", Labels: []string{"ready-for-agent", "Feature"}, Parent: "COD-805"},
		{ID: "COD-2", Title: "Second", State: "Backlog", HasChildren: true},
	}
	if err := writeEligibleJSON(&buf, tickets); err != nil {
		t.Fatalf("writeEligibleJSON: %v", err)
	}

	var got []eligibleTicket
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, buf.String())
	}
	if len(got) != 2 {
		t.Fatalf("decoded %d tickets, want 2", len(got))
	}
	if got[0].ID != "COD-1" || got[0].Title != "First" {
		t.Errorf("ticket[0] = %+v, want COD-1/First", got[0])
	}
	if len(got[0].Labels) != 2 || got[0].Labels[0] != "ready-for-agent" || got[0].Labels[1] != "Feature" {
		t.Errorf("ticket[0].Labels = %v, want [ready-for-agent Feature]", got[0].Labels)
	}
	if got[0].Parent != "COD-805" || got[0].HasChildren {
		t.Errorf("ticket[0] hierarchy = (%q, %v), want (COD-805, false)", got[0].Parent, got[0].HasChildren)
	}
	if got[1].Labels == nil {
		t.Errorf("ticket[1].Labels = nil, want an empty array so the shape is stable")
	}
	if got[1].Parent != "" || !got[1].HasChildren {
		t.Errorf("ticket[1] hierarchy = (%q, %v), want (empty, true)", got[1].Parent, got[1].HasChildren)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"labels":[]`)) {
		t.Errorf("labelless ticket should serialize labels as [], got %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"parent":""`)) || !bytes.Contains(buf.Bytes(), []byte(`"has_children":false`)) {
		t.Errorf("parent and has_children must always be present, got %q", buf.String())
	}
}

func TestWriteEligibleJSONEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := writeEligibleJSON(&buf, nil); err != nil {
		t.Fatalf("writeEligibleJSON(nil): %v", err)
	}
	if got := bytes.TrimSpace(buf.Bytes()); string(got) != "[]" {
		t.Errorf("empty queue = %q, want []", got)
	}
}

func TestGroupEligibleLinesFlatWhenNoParent(t *testing.T) {
	tickets := []tracker.ListedTicket{
		{ID: "COD-1", Title: "First", Labels: []string{"ready-for-agent", "Feature"}},
		{ID: "COD-2", Title: "Second"},
		{ID: "COD-3", Title: "An epic", HasChildren: true},
	}
	want := []string{
		"COD-1  First  [ready-for-agent, Feature]",
		"COD-2  Second",
		"COD-3  An epic",
	}
	got := groupEligibleLines(tickets)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flat output = %#v, want %#v", got, want)
	}
}

func TestGroupEligibleLinesGroupsUnderEpics(t *testing.T) {
	tickets := []tracker.ListedTicket{
		{ID: "COD-100", Title: "Epic one", Labels: []string{"epic"}, HasChildren: true},
		{ID: "COD-101", Title: "Child A", Labels: []string{"ready-for-agent"}, Parent: "COD-100"},
		{ID: "COD-102", Title: "Child B", Parent: "COD-100"},
		{ID: "COD-4", Title: "Top-level"},
		{ID: "COD-201", Title: "Orphan child", Parent: "COD-200"},
	}
	want := []string{
		"COD-100  Epic one  [epic]",
		"  COD-101  Child A  [ready-for-agent]",
		"  COD-102  Child B",
		"COD-4  Top-level",
		"COD-200",
		"  COD-201  Orphan child",
	}
	got := groupEligibleLines(tickets)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grouped output = %#v, want %#v", got, want)
	}
}

func TestGroupEligibleLinesChildBeforeEpic(t *testing.T) {
	tickets := []tracker.ListedTicket{
		{ID: "COD-11", Title: "Child", Parent: "COD-10"},
		{ID: "COD-10", Title: "Epic", HasChildren: true},
	}
	want := []string{
		"COD-10  Epic",
		"  COD-11  Child",
	}
	got := groupEligibleLines(tickets)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child-before-epic output = %#v, want %#v", got, want)
	}
}
