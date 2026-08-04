package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/budget"
	"github.com/RomkaLTU/trau/internal/state"
)

// spentLedger is already past any cap a test configures, so the budget guard trips
// on the run's very first agent call.
type spentLedger struct {
	tokens int
	cost   float64
}

func (spentLedger) SetTicket(string) {}

func (l spentLedger) Total(string) (int, float64, bool) { return l.tokens, l.cost, true }

func (l spentLedger) DayTotal(string) (int, float64, bool) { return l.tokens, l.cost, true }

// TestBudgetCapHaltsResumablyRatherThanQuarantining is the COD-1480 regression
// guard: a spend ceiling reached mid-run is transient and says nothing about the
// ticket, so it must preserve the work on the feature branch and leave the ticket
// at its checkpoint for the next run — never quarantine it behind a human.
func TestBudgetCapHaltsResumablyRatherThanQuarantining(t *testing.T) {
	id := "COD-1480"
	tr := &fakeTracker{}
	git := &recordingGit{branch: "feature/COD-1480-x"}
	p := newTestPipeline(t, fakeRunner{}, tr)
	p.Git = git
	p.Remote = "origin"
	p.Tokens = spentLedger{tokens: 120_000, cost: 0.62}
	p.Budget = budget.Limits{TicketUSD: 0.50}
	if err := p.State.Set(id, "BRANCH", git.branch); err != nil {
		t.Fatal(err)
	}

	err := p.Process(context.Background(), id)

	b := AsBudgetHalt(err)
	if b == nil {
		t.Fatalf("Process err = %v, want a *BudgetError", err)
	}
	if isGiveUp(err) || IsFault(err) {
		t.Errorf("a spent budget must read as neither a give-up nor a fault: %v", err)
	}
	if !strings.Contains(b.Reason, "per-ticket budget") {
		t.Errorf("reason = %q, want it to name the cap that tripped", b.Reason)
	}

	if tr.quarantineCalls != 0 || tr.fileBugCalls != 0 {
		t.Errorf("Quarantine=%d FileBug=%d, want 0/0 — a cap resets, and the ticket is not at fault",
			tr.quarantineCalls, tr.fileBugCalls)
	}
	if got := p.State.Get(id, "PHASE"); got != state.Building {
		t.Errorf("PHASE = %q, want it left at its checkpoint (%q)", got, state.Building)
	}
	if got := p.State.Get(id, "FAILURE_CLASS"); got != state.FailBudget {
		t.Errorf("FAILURE_CLASS = %q, want %q so every surface reads the halt as a budget stop", got, state.FailBudget)
	}

	if len(git.commitMsgs) != 1 || !strings.Contains(git.commitMsgs[0], id) {
		t.Fatalf("commit msgs = %v, want the WIP preserved on %s", git.commitMsgs, git.branch)
	}
	rid, rphase := p.State.ResumeTargetFunc(nil)
	if rid != id || rphase != state.Building {
		t.Errorf("resume target = (%q, %q), want (%q, %q) so the next run continues the ticket",
			rid, rphase, id, state.Building)
	}
}

// TestVerifyBudgetCapHaltsInsteadOfQuarantining covers the phases that treat an
// agent error as the agent's own verdict: a cap tripped there must propagate as a
// halt rather than be recorded as a verify failure, which would burn the repair
// attempts and cascade into the quarantine + HITL bug the halt exists to avoid.
func TestVerifyBudgetCapHaltsInsteadOfQuarantining(t *testing.T) {
	id := "COD-90584"
	writeHandoff(t, id)
	tr := &fakeTracker{}
	p := newTestPipeline(t, fakeRunner{}, tr)
	p.Tokens = spentLedger{tokens: 120_000, cost: 0.62}
	p.Budget = budget.Limits{TicketUSD: 0.50}

	err := p.Verify(context.Background(), id)

	if !IsBudgetHalt(err) {
		t.Fatalf("Verify err = %v, want a *BudgetError", err)
	}
	if tr.quarantineCalls != 0 || tr.fileBugCalls != 0 {
		t.Errorf("Quarantine=%d FileBug=%d, want 0/0 — a cap is not the ticket failing verify",
			tr.quarantineCalls, tr.fileBugCalls)
	}
	if got := p.State.Get(id, "PHASE"); got == state.Quarantined {
		t.Errorf("PHASE = quarantined, want it left in-flight for the next run to resume")
	}
}
