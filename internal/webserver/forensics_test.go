package webserver

import (
	"context"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// TestForensicsRunsThroughHub confirms the run board read surfaces phase and the
// failure class/reason for a faulted run and clears the reason for a merged one.
func TestForensicsRunsThroughHub(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	hub := hubclient.New(ts.URL, "")
	ctx := context.Background()

	put := func(ticket string, data map[string]string) {
		if err := hub.PutCheckpoint(ctx, "acme", ticket, hubclient.Checkpoint{Data: data}); err != nil {
			t.Fatalf("PutCheckpoint %s: %v", ticket, err)
		}
	}
	put("COD-1", map[string]string{"PHASE": "building", "FAILURE_REASON": "agent stalled", "FAILURE_CLASS": "faulted"})
	put("COD-2", map[string]string{"PHASE": "merged", "FAILURE_REASON": "was set earlier"})

	runs, err := hub.Runs(ctx, "acme")
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	byID := map[string]hubclient.RunSummary{}
	for _, r := range runs {
		byID[r.Ticket] = r
	}

	faulted := byID["COD-1"]
	if faulted.Phase != "building" || faulted.FailureClass != "faulted" || faulted.FailureReason != "agent stalled" {
		t.Fatalf("faulted run = %+v, want building/faulted/agent stalled", faulted)
	}
	merged := byID["COD-2"]
	if merged.Phase != "merged" || merged.FailureReason != "" {
		t.Fatalf("merged run = %+v, want merged with no reason", merged)
	}
}

// TestForensicsEventsQueryThroughHub drives the forensics event read: filters by
// ticket, kind, and a grep pattern, and pages forward from a cursor for a follow.
func TestForensicsEventsQueryThroughHub(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	hub := hubclient.New(ts.URL, "")
	ctx := context.Background()

	if err := hub.AppendEvents(ctx, "acme", []hubclient.Event{
		{TS: "2026-07-12T10:00:00Z", Kind: "agent_start", Phase: "build", Msg: "start"},
		{TS: "2026-07-12T10:00:30Z", Kind: "state_change", Phase: "build", Fields: `{"ticket":"COD-10","state":"merged"}`},
		{TS: "2026-07-12T10:01:00Z", Kind: "state_change", Phase: "build", Fields: `{"ticket":"COD-1","state":"faulted","reason":"boom"}`},
		{TS: "2026-07-12T10:02:00Z", Kind: "agent_call", Phase: "verify", Msg: "COD-1 verify call"},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	// COD-1 must not bleed into COD-10, whether it carries a structured ticket or a msg mention.
	byTicket, err := hub.QueryEvents(ctx, "acme", hubclient.EventQuery{Ticket: "COD-1"})
	if err != nil {
		t.Fatalf("QueryEvents ticket: %v", err)
	}
	if len(byTicket) != 2 {
		t.Fatalf("COD-1 events = %d, want 2 (no prefix bleed into COD-10)", len(byTicket))
	}
	longer, err := hub.QueryEvents(ctx, "acme", hubclient.EventQuery{Ticket: "COD-10"})
	if err != nil {
		t.Fatalf("QueryEvents COD-10: %v", err)
	}
	if len(longer) != 1 {
		t.Fatalf("COD-10 events = %d, want 1", len(longer))
	}

	faulted, err := hub.QueryEvents(ctx, "acme", hubclient.EventQuery{Kind: "state_change", Grep: "FAULTED"})
	if err != nil {
		t.Fatalf("QueryEvents grep: %v", err)
	}
	if len(faulted) != 1 || faulted[0].Fields["reason"] != "boom" {
		t.Fatalf("grep faulted = %+v, want the one state_change with reason boom", faulted)
	}

	// A follow poll pages strictly past the last id seen.
	after := faulted[0].ID
	tail, err := hub.QueryEvents(ctx, "acme", hubclient.EventQuery{After: mustID(t, after)})
	if err != nil {
		t.Fatalf("QueryEvents after: %v", err)
	}
	if len(tail) != 1 || tail[0].Kind != "agent_call" {
		t.Fatalf("after-cursor tail = %+v, want the agent_call", tail)
	}
}

// TestForensicsSpendThroughHub confirms the spend summary's total matches the status
// view's TokenTotal and that the per-phase breakdown sums to it.
func TestForensicsSpendThroughHub(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	hub := hubclient.New(ts.URL, "")
	ctx := context.Background()

	if err := hub.AppendTokenCalls(ctx, "acme", []hubclient.TokenCall{
		{Ticket: "COD-1", TS: "2026-07-12T10:00:00", Phase: "build", Total: 500, CostUSD: usd(0.50), Turns: 3},
		{Ticket: "COD-1", TS: "2026-07-12T10:01:00", Phase: "verify", Total: 200, CostUSD: usd(0.20), Turns: 1},
	}); err != nil {
		t.Fatalf("AppendTokenCalls: %v", err)
	}

	summary, err := hub.TicketSpend(ctx, "acme", "COD-1")
	if err != nil {
		t.Fatalf("TicketSpend: %v", err)
	}
	total, err := hub.TokenTotal(ctx, "acme", "COD-1")
	if err != nil {
		t.Fatalf("TokenTotal: %v", err)
	}
	if summary.Total != total {
		t.Fatalf("summary total = %+v, want it to match status total %+v", summary.Total, total)
	}
	if len(summary.Phases) != 2 {
		t.Fatalf("phases = %d, want build + verify", len(summary.Phases))
	}
	var phaseTokens int
	var phaseCost float64
	for _, p := range summary.Phases {
		phaseTokens += p.Tokens
		phaseCost += p.Cost
	}
	if phaseTokens != total.Tokens || phaseCost != total.Cost {
		t.Fatalf("phase sum = (%d, %.2f), want the ticket total (%d, %.2f)", phaseTokens, phaseCost, total.Tokens, total.Cost)
	}
}

func mustID(t *testing.T, id string) int64 {
	t.Helper()
	n, ok := parseCursor(id)
	if !ok {
		t.Fatalf("event id %q is not a cursor", id)
	}
	return n
}
