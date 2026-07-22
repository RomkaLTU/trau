package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

// SteerQueue is a ticket's operator steer-note queue: what no agent has taken,
// the ack that claims one for a phase, and the sweep that drops whatever is left
// once the run settles. Every call is best-effort — a failure is logged and the
// phase carries on (ADR 0008).
type SteerQueue interface {
	Pending(ctx context.Context, ticket string) ([]hubclient.SteerNote, error)
	Ack(ctx context.Context, id int64, phase string) error
	Expire(ctx context.Context, ticket string) ([]hubclient.SteerNote, error)
}

// steerHeading opens the block a boundary delivery appends to a phase prompt.
const steerHeading = "## Operator steer notes (typed mid-run)"

// steerPreamble tells the agent these arrived after the run started, so they
// outrank the ticket text they qualify.
const steerPreamble = "The operator typed these while this ticket was already running, so they are newer than everything above and override it where they disagree. Fold them into the work you are about to do."

// steerSweepBudget bounds the settle sweep. It runs detached from the run's
// context — a stop is one of the things that settles a run — so it needs a
// deadline of its own.
const steerSweepBudget = 10 * time.Second

// steerSection drains the ticket's queued steer notes into a block for the phase
// prompt about to be composed, acking each under phase. This is the delivery
// every provider shares — a note typed between phases, or into a provider that
// cannot be typed into mid-session, reaches the next substantive agent here.
// Returns "" when the queue is empty or unreachable.
func (p *Pipeline) steerSection(ctx context.Context, id, phase string) string {
	if p.Steer == nil {
		return ""
	}
	notes, err := p.Steer.Pending(ctx, id)
	if err != nil {
		p.logf("  steer notes unavailable (continuing): %v", err)
		return ""
	}
	if len(notes) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n\n%s\n\n%s\n", steerHeading, steerPreamble)
	for _, n := range notes {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", n.CreatedAt, strings.TrimSpace(n.Body))
		if err := p.Steer.Ack(ctx, n.ID, phase); err != nil {
			p.logf("  steer note %d ack error (continuing): %v", n.ID, err)
			continue
		}
		p.emitSteerDelivered(id, n.ID, phase)
	}
	p.logf("  ✎ %d steer note(s) handed to %s", len(notes), phase)
	return b.String()
}

// steerSource binds the ticket's queue to the agent seam, so a provider that can
// be typed into while it works polls the same queue steerSection drained at spawn
// time.
func (p *Pipeline) steerSource(id string) agent.SteerSource {
	if p.Steer == nil {
		return agent.SteerSource{}
	}
	return agent.SteerSource{
		Ticket: id,
		Pending: func(ctx context.Context) ([]agent.SteerNote, error) {
			notes, err := p.Steer.Pending(ctx, id)
			if err != nil {
				return nil, err
			}
			out := make([]agent.SteerNote, 0, len(notes))
			for _, n := range notes {
				out = append(out, agent.SteerNote{ID: n.ID, Body: n.Body})
			}
			return out, nil
		},
		Ack: func(ctx context.Context, noteID int64, phase string) error {
			return p.Steer.Ack(ctx, noteID, phase)
		},
	}
}

// expireSteer sweeps the ticket's undelivered steer notes once its run settles —
// merged, quarantined, or faulted. Notes never carry across tickets, so what the
// operator typed too late is dropped rather than handed to whatever runs next.
func (p *Pipeline) expireSteer(ctx context.Context, id string) {
	if p.Steer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerSweepBudget)
	defer cancel()

	notes, err := p.Steer.Expire(ctx, id)
	if err != nil {
		p.logf("  steer note sweep error (continuing): %v", err)
		return
	}
	for _, n := range notes {
		p.emitSteerExpired(id, n.ID)
	}
}

func (p *Pipeline) emitSteerDelivered(id string, noteID int64, phase string) {
	if p.Events == nil {
		return
	}
	p.Events.Emit(event.KindSteerDelivered, phase, "", map[string]any{
		"ticket":    id,
		"note_id":   noteID,
		"mid_phase": false,
	})
}

func (p *Pipeline) emitSteerExpired(id string, noteID int64) {
	if p.Events == nil {
		return
	}
	p.Events.Emit(event.KindSteerExpired, "", "", map[string]any{
		"ticket":  id,
		"note_id": noteID,
	})
}
