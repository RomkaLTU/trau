package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// fakeSteerQueue stands in for the hub's steer-note queue: it hands out the same
// pending notes until they are acked, and records every ack and sweep.
type fakeSteerQueue struct {
	mu      sync.Mutex
	pending []hubclient.SteerNote
	acks    []steerAck
	sweeps  int
	pendErr error
	ackErr  error
}

type steerAck struct {
	id    int64
	phase string
}

func (q *fakeSteerQueue) Pending(context.Context, string) ([]hubclient.SteerNote, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pendErr != nil {
		return nil, q.pendErr
	}
	return append([]hubclient.SteerNote(nil), q.pending...), nil
}

func (q *fakeSteerQueue) Ack(_ context.Context, id int64, phase string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ackErr != nil {
		return q.ackErr
	}
	q.acks = append(q.acks, steerAck{id: id, phase: phase})
	q.pending = nil
	return nil
}

func (q *fakeSteerQueue) Expire(ctx context.Context, _ string) ([]hubclient.SteerNote, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweeps++
	swept := q.pending
	q.pending = nil
	return swept, nil
}

func (q *fakeSteerQueue) acked() []steerAck {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]steerAck(nil), q.acks...)
}

func (q *fakeSteerQueue) swept() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sweeps
}

func steeredPipeline(t *testing.T, q SteerQueue) (*Pipeline, *promptLog) {
	t.Helper()
	log := &promptLog{}
	p := newTestPipeline(t, fakeRunner{calls: log}, &fakeTracker{})
	p.Steer = q
	return p, log
}

const steerBody = "the test DB is on now — use it instead of the fixture"

func pendingNote(id int64) hubclient.SteerNote {
	return hubclient.SteerNote{ID: id, Ticket: "COD-1", Body: steerBody, CreatedAt: "2026-07-23T09:00:00Z", Status: hubclient.SteerPending}
}

// TestSteerNotesReachSubstantivePhasePrompts pins the next-spawn fallback:
// queued notes are appended to the next substantive phase's prompt and acked
// under it, while mechanical phases never drain the queue.
func TestSteerNotesReachSubstantivePhasePrompts(t *testing.T) {
	cases := []struct {
		phase     string
		delivered bool
	}{
		{phase: "build", delivered: true},
		{phase: "verify", delivered: true},
		{phase: "repair1", delivered: true},
		{phase: "bugfix1", delivered: true},
		{phase: "commit", delivered: false},
		{phase: "cleanup", delivered: false},
		{phase: "lintfix", delivered: false},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(4)}}
			p, log := steeredPipeline(t, q)

			if _, err := p.agentStep(context.Background(), "COD-1", tc.phase, "PHASE PROMPT"); err != nil {
				t.Fatalf("agentStep: %v", err)
			}

			calls := log.all()
			if len(calls) != 1 {
				t.Fatalf("runner called %d times, want 1", len(calls))
			}
			got := strings.Contains(calls[0].prompt, steerBody)
			if got != tc.delivered {
				t.Errorf("prompt carries the steer note = %v, want %v\nprompt: %s", got, tc.delivered, calls[0].prompt)
			}
			if tc.delivered && !strings.Contains(calls[0].prompt, steerHeading) {
				t.Error("steer notes were appended without their delimiting heading")
			}

			acks := q.acked()
			switch {
			case tc.delivered && len(acks) != 1:
				t.Fatalf("acked %d notes, want 1", len(acks))
			case tc.delivered && acks[0] != (steerAck{id: 4, phase: tc.phase}):
				t.Errorf("ack = %+v, want note 4 acked under %s", acks[0], tc.phase)
			case !tc.delivered && len(acks) != 0:
				t.Errorf("mechanical phase acked %d notes, want 0", len(acks))
			}
		})
	}
}

// TestSteerNotesDrainedOnceAcrossRetries pins that the drain happens while the
// prompt is composed, not per attempt.
func TestSteerNotesDrainedOnceAcrossRetries(t *testing.T) {
	q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(11)}}
	p, log := steeredPipeline(t, q)
	p.Runner = fakeRunner{err: errors.New("agent crashed: boom"), calls: log}
	p.AgentRetries = 2

	if _, err := p.agentStep(context.Background(), "COD-1", "build", "PHASE PROMPT"); err == nil {
		t.Fatal("agentStep err = nil, want the exhausted-recovery error")
	}

	calls := log.all()
	if len(calls) != 3 {
		t.Fatalf("runner called %d times, want 3 (one attempt plus two retries)", len(calls))
	}
	for i, c := range calls {
		if !strings.Contains(c.prompt, steerBody) {
			t.Errorf("attempt %d lost the steer note", i+1)
		}
	}
	if acks := q.acked(); len(acks) != 1 {
		t.Errorf("acked %d notes across three attempts, want 1", len(acks))
	}
}

// TestSteerHubFailureNeverFailsPhase pins the best-effort contract on the
// pipeline side: an unreachable queue costs the phase its notes, never its run.
func TestSteerHubFailureNeverFailsPhase(t *testing.T) {
	q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(5)}, pendErr: errors.New("hub unreachable")}
	p, log := steeredPipeline(t, q)

	if _, err := p.agentStep(context.Background(), "COD-1", "build", "PHASE PROMPT"); err != nil {
		t.Fatalf("agentStep: %v", err)
	}
	calls := log.all()
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(calls))
	}
	if strings.Contains(calls[0].prompt, steerHeading) {
		t.Error("prompt carries a steer section built from an unreadable queue")
	}
}

// TestSteerAckFailureStillDeliversNote: the note is already in the prompt the
// agent is about to read, so a failed ack must not hold it back.
func TestSteerAckFailureStillDeliversNote(t *testing.T) {
	q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(6)}, ackErr: errors.New("hub unreachable")}
	p, log := steeredPipeline(t, q)

	if _, err := p.agentStep(context.Background(), "COD-1", "build", "PHASE PROMPT"); err != nil {
		t.Fatalf("agentStep: %v", err)
	}
	if calls := log.all(); !strings.Contains(calls[0].prompt, steerBody) {
		t.Error("prompt dropped the steer note because its ack failed")
	}
}

// TestSettleExpiresLeftoverSteerNotes: a note nobody consumed is swept on every
// settle path, so it can never surface inside the next ticket.
func TestSettleExpiresLeftoverSteerNotes(t *testing.T) {
	settle := map[string]func(p *Pipeline, id string){
		"merged": func(p *Pipeline, id string) {
			if err := p.markDone(context.Background(), id, "  ✓ merged %s"); err != nil {
				t.Fatalf("markDone: %v", err)
			}
		},
		"quarantined": func(p *Pipeline, id string) {
			_ = p.giveUp(context.Background(), id, "repairs exhausted")
		},
		"faulted": func(p *Pipeline, id string) {
			_ = p.fault(context.Background(), id, errors.New("boom"))
		},
	}
	for name, run := range settle {
		t.Run(name, func(t *testing.T) {
			q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(8)}}
			p, _ := steeredPipeline(t, q)

			run(p, "COD-1")

			if got := q.swept(); got != 1 {
				t.Errorf("sweeps = %d, want 1 — a settled run must not leave notes queued", got)
			}
		})
	}
}

// TestSettleSweepSurvivesACancelledRun: cancelling the run is one of the things
// that settles it, so the sweep runs detached from that cancellation.
func TestSettleSweepSurvivesACancelledRun(t *testing.T) {
	q := &fakeSteerQueue{pending: []hubclient.SteerNote{pendingNote(12)}}
	p, _ := steeredPipeline(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = p.fault(ctx, "COD-1", errors.New("stopped"))

	if got := q.swept(); got != 1 {
		t.Errorf("sweeps = %d, want 1 on a cancelled run", got)
	}
}
