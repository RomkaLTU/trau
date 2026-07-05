package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/RomkaLTU/trau/internal/tracker"
)

func TestWriteEligibleJSON(t *testing.T) {
	var buf bytes.Buffer
	tickets := []tracker.ListedTicket{
		{ID: "COD-1", Title: "First", State: "Todo", Labels: []string{"ready-for-agent", "Feature"}},
		{ID: "COD-2", Title: "Second", State: "Backlog"},
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
	if got[1].Labels == nil {
		t.Errorf("ticket[1].Labels = nil, want an empty array so the shape is stable")
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"labels":[]`)) {
		t.Errorf("labelless ticket should serialize labels as [], got %q", buf.String())
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
