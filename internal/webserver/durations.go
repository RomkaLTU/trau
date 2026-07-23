package webserver

import (
	"sort"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// StepDuration is one display Step's derived wall-clock, summed from the
// activity_change deltas of the Activities that fall under it (ADR 0009). Durations
// are never stored — they derive from event timestamps at read time — so a run
// predating the activity_change signal carries none and the page shows no guess.
type StepDuration struct {
	Step       string `json:"step"`
	DurationMS int64  `json:"duration_ms"`
}

// activityStart is one activity_change: the instant a session began an Activity.
type activityStart struct {
	at  time.Time
	act activity.Activity
}

// terminalStates are the state_change values that close a run and so end the last
// Activity's wall-clock (ADR 0008 §7, ADR 0009). Mirrors the web's TERMINAL_STATES.
var terminalStates = map[string]bool{
	"merged":      true,
	"faulted":     true,
	"quarantined": true,
	"paused":      true,
	"stopped":     true,
}

const maxDurationEvents = 5000

// runStepDurations derives a ticket's per-Step wall-clock from its activity_change
// deltas (ADR 0009): an Activity runs until the next activity_change or, for the
// last one, until the run's terminal state_change. The spans group into their
// display Step. A run with no activity_change events — one predating the signal —
// yields nil so the page shows no durations. A store error degrades to nil rather
// than failing the resource.
func runStepDurations(evs *hubstore.Events, root, ticket string) []StepDuration {
	acts, err := evs.Query(root, hubstore.EventFilter{Kind: "activity_change", Ticket: ticket, Limit: maxDurationEvents})
	if err != nil {
		logger.Verbosef("step durations %s/%s: %v", root, ticket, err)
		return nil
	}
	if len(acts) == 0 {
		return nil
	}
	states, err := evs.Query(root, hubstore.EventFilter{Kind: "state_change", Ticket: ticket, Limit: maxDurationEvents})
	if err != nil {
		logger.Verbosef("step durations %s/%s: %v", root, ticket, err)
	}

	starts := make([]activityStart, 0, len(acts))
	for _, r := range acts {
		at, ok := parseEventTime(r.TS)
		if !ok {
			continue
		}
		fields := unmarshalFields(r.Fields)
		name, _ := fields["activity"].(string)
		starts = append(starts, activityStart{at: at, act: activity.Activity(name)})
	}

	terminals := make([]time.Time, 0, len(states))
	for _, r := range states {
		fields := unmarshalFields(r.Fields)
		if st, _ := fields["state"].(string); !terminalStates[st] {
			continue
		}
		if at, ok := parseEventTime(r.TS); ok {
			terminals = append(terminals, at)
		}
	}

	return stepDurations(starts, terminals)
}

// stepDurations groups the derived per-Activity spans into their Steps, scoped to
// the latest run so the totals track the run's displayed wall-clock even after a
// pause and resume. The current run is the span of activity_change events after the
// terminal state_change that precedes the most recent one, closed by the terminal
// state_change that follows it — or, for a run still in flight, left open at its
// last Activity (which then contributes nothing until the next change).
func stepDurations(starts []activityStart, terminals []time.Time) []StepDuration {
	if len(starts) == 0 {
		return nil
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].at.Before(starts[j].at) })
	last := starts[len(starts)-1].at

	var prior, end time.Time
	haveEnd := false
	for _, t := range terminals {
		if t.Before(last) {
			if t.After(prior) {
				prior = t
			}
			continue
		}
		if !haveEnd || t.Before(end) {
			end, haveEnd = t, true
		}
	}
	if !haveEnd {
		end = last
	}

	byStep := make(map[activity.Step]time.Duration, len(activity.Steps))
	current := make([]activityStart, 0, len(starts))
	for _, a := range starts {
		if a.at.After(prior) && !a.at.After(end) {
			current = append(current, a)
		}
	}
	for i, a := range current {
		next := end
		if i+1 < len(current) {
			next = current[i+1].at
		}
		if d := next.Sub(a.at); d > 0 {
			byStep[activity.StepOf(a.act)] += d
		}
	}

	out := make([]StepDuration, 0, len(activity.Steps))
	for _, st := range activity.Steps {
		if d := byStep[st]; d > 0 {
			out = append(out, StepDuration{Step: string(st), DurationMS: d.Milliseconds()})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseEventTime reads an event's stored timestamp, accepting the RFC3339 form the
// loop emits and the zoneless form some fixtures carry. ok is false when neither
// layout parses.
func parseEventTime(ts string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
