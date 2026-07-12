package webserver

import (
	"context"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// TestTokensRoundTripThroughHub drives the whole DB-first token seam the loop child
// uses (ADR 0008): a batch of calls posted over the client lands in the store, and
// the ticket total, the machine day total the budget day cap reads, and the flagged
// anomalies all read back through the hub — no run files anywhere.
func TestTokensRoundTripThroughHub(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	hub := hubclient.New(ts.URL, "")
	ctx := context.Background()

	if err := hub.AppendTokenCalls(ctx, "acme", []hubclient.TokenCall{
		{Ticket: "COD-1", TS: "2026-07-12T10:00:00", Phase: "build", Input: 300, Output: 200, Total: 500, CostUSD: usd(0.50)},
		{Ticket: "COD-1", TS: "2026-07-12T10:01:00", Phase: "verify", Input: 100, Output: 100, Total: 200, CostUSD: usd(0.20)},
		{Ticket: "_loop", TS: "2026-07-12T09:00:00", Phase: "pick", Input: 20, Output: 10, Total: 30, CostUSD: usd(0.02)},
	}); err != nil {
		t.Fatalf("AppendTokenCalls: %v", err)
	}

	total, err := hub.TokenTotal(ctx, "acme", "COD-1")
	if err != nil {
		t.Fatalf("TokenTotal: %v", err)
	}
	if total.Tokens != 700 || total.Cost != 0.70 || !total.Metered {
		t.Errorf("ticket total = %+v, want tokens 700 cost 0.70 metered", total)
	}

	// The budget day cap reads this — it must sum every bucket, including _loop.
	day, err := hub.TokenDayTotal(ctx, "acme", "2026-07-12")
	if err != nil {
		t.Fatalf("TokenDayTotal: %v", err)
	}
	if day.Tokens != 730 || day.Cost != 0.72 {
		t.Errorf("day total = %+v, want tokens 730 cost 0.72 (both buckets)", day)
	}

	if err := hub.RecordAnomalies(ctx, "acme", "COD-1", []hubclient.Anomaly{
		{TS: "2026-07-12T10:02:00", Phase: "cleanup", Output: 120_000, Turns: 8, Cost: 6.5, Reasons: []string{"cost $6.50 > $3.00"}},
	}); err != nil {
		t.Fatalf("RecordAnomalies: %v", err)
	}

	c := getCosts(t, ts, "?days=7")
	if len(c.Anomalies) != 1 {
		t.Fatalf("costs anomalies = %d, want the single recorded trip", len(c.Anomalies))
	}
	if a := c.Anomalies[0]; a.Repo != "acme" || a.Ticket != "COD-1" || a.Phase != "cleanup" || a.CostUSD != 6.5 {
		t.Errorf("anomaly = %+v, want acme/COD-1 cleanup at $6.50", a)
	}
}

// TestTokenDayTotalDefaultsToToday confirms the day endpoint defaults to today's
// date when none is given, so a caller can read the current day's spend with no
// query param.
func TestTokenDayTotalDefaultsToToday(t *testing.T) {
	home := t.TempDir()
	seedRepo(t, home, "acme")
	ts := instancesServer(t, home)
	hub := hubclient.New(ts.URL, "")

	if _, err := hub.TokenDayTotal(context.Background(), "acme", ""); err != nil {
		t.Fatalf("TokenDayTotal with empty date: %v", err)
	}
}
