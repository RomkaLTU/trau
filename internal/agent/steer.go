package agent

import (
	"context"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/logger"
)

// SteerNote is one operator message queued against the ticket a session is
// working, in the delivery order the hub fixed when it was typed.
type SteerNote struct {
	ID   int64
	Body string
}

// SteerSource is a running ticket's steer-note queue as an agent session sees it:
// Pending reads what no agent has taken, Ack claims one under the phase that
// consumed it. Both are best-effort — an error from either leaves the phase
// running untouched. Only a backend that can be typed into mid-session acts on a
// source; the rest ignore it and rely on the pipeline's next-spawn delivery.
type SteerSource struct {
	Ticket  string
	Pending func(ctx context.Context) ([]SteerNote, error)
	Ack     func(ctx context.Context, id int64, phase string) error
}

type steerKey struct{}

// WithSteer binds a ticket's steer-note queue to the context driving one agent
// call. An incomplete source binds nothing.
func WithSteer(ctx context.Context, src SteerSource) context.Context {
	if src.Pending == nil || src.Ack == nil {
		return ctx
	}
	return context.WithValue(ctx, steerKey{}, src)
}

// SteerFrom returns the steer-note queue bound to ctx, if one is.
func SteerFrom(ctx context.Context) (SteerSource, bool) {
	src, ok := ctx.Value(steerKey{}).(SteerSource)
	return src, ok
}

// steerPollInterval is how often a live session re-reads the ticket's queue.
// Notes are typed by hand, so seconds of latency cost nothing.
const steerPollInterval = 3 * time.Second

// steerKeystrokes renders a note as a bracketed paste — so an embedded newline
// lands in the message instead of submitting it early — followed by the CR that
// sends it.
func steerKeystrokes(body string) []byte {
	return []byte("\x1b[200~" + body + "\x1b[201~\r")
}

func steerInterval(poll time.Duration) time.Duration {
	if poll > 0 {
		return poll
	}
	return steerPollInterval
}

// deliverSteer types the operator's queued notes into a session that is already
// working, acking only what the terminal actually took. A hub error is logged and
// dropped — steering must never end a phase.
func deliverSteer(ctx context.Context, sess terminalSession, src SteerSource, label string, poll time.Duration, log *event.Log) {
	tick := time.NewTicker(steerInterval(poll))
	defer tick.Stop()
	// A note whose ack failed is still in the terminal, so a later poll that
	// still finds it pending must not type it again.
	typed := map[int64]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		notes, err := src.Pending(ctx)
		if err != nil {
			logger.Verbosef("steer notes not read for %s (%s): %v", src.Ticket, label, err)
			continue
		}
		for _, n := range notes {
			if typed[n.ID] {
				continue
			}
			if _, err := sess.Write(steerKeystrokes(n.Body)); err != nil {
				return
			}
			typed[n.ID] = true
			if err := src.Ack(ctx, n.ID, label); err != nil {
				logger.Verbosef("steer note %d not acked (%s): %v", n.ID, label, err)
				continue
			}
			announceSteer(log, src.Ticket, n.ID, label)
		}
	}
}

// announceSteer records a note the live session took, so the run timeline tells
// mid-phase delivery from next-spawn delivery.
func announceSteer(log *event.Log, ticket string, id int64, label string) {
	if log == nil {
		return
	}
	log.Emit(event.KindSteerDelivered, label, "", map[string]any{
		"ticket":    ticket,
		"note_id":   id,
		"mid_phase": true,
	})
}
