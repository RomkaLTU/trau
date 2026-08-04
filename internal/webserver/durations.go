package webserver

import (
	"sort"
	"time"

	"github.com/RomkaLTU/trau/internal/activity"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/teamsync"
)

// StepDuration is one display Step's derived wall-clock, summed from the
// activity_change deltas of the Activities that fall under it (ADR 0009). Durations
// are never stored — they derive from event timestamps at read time — so a run
// predating the activity_change signal carries none and the page shows no guess.
type StepDuration struct {
	Step       string `json:"step"`
	DurationMS int64  `json:"duration_ms"`
}

// ActivitySpan is one Activity's derived wall-clock inside a run — the finer grain
// under StepDuration. It is the wire type itself: the timeline shared run history
// travels with is the shape the API serves back, so neither side converts.
type ActivitySpan = teamsync.ActivitySpan

// activityStart is one activity_change: the instant a session began an Activity.
type activityStart struct {
	at  time.Time
	act activity.Activity
}

// activitySpan is one Activity closed against whatever followed it.
type activitySpan struct {
	act activity.Activity
	d   time.Duration
}

// terminalStates are the state_change values that close a run and so end the last
// Activity's wall-clock (ADR 0008 §7, ADR 0009). Mirrors the web's TERMINAL_STATES.
var terminalStates = map[string]bool{
	"merged":      true,
	"faulted":     true,
	"quarantined": true,
	"paused":      true,
	"stopped":     true,
	"budget":      true,
}

const maxDurationEvents = 5000

// runStepDurations derives a ticket's per-Step wall-clock from its activity_change
// deltas (ADR 0009): an Activity runs until the next activity_change or, for the
// last one, until the run's terminal state_change. The spans group into their
// display Step. A run with no activity_change events — one predating the signal —
// yields nil so the page shows no durations. A store error degrades to nil rather
// than failing the resource.
func runStepDurations(evs *hubstore.Events, root, ticket string) []StepDuration {
	starts, terminals := runActivityEvents(evs, root, ticket)
	return stepDurations(starts, terminals)
}

// runActivitySpans derives a ticket's per-Activity wall-clock from the same deltas,
// alongside the instant its latest run's first Activity began. It is the compact
// timeline shared run history carries in place of the event stream it came from.
func runActivitySpans(evs *hubstore.Events, root, ticket string) (startedAt string, spans []ActivitySpan) {
	starts, terminals := runActivityEvents(evs, root, ticket)
	current, end := currentRun(starts, terminals)
	if len(current) == 0 {
		return "", nil
	}
	return current[0].at.UTC().Format(time.RFC3339), activitySpans(current, end)
}

// runActivityEvents reads the two signals a run's wall-clock derives from: the
// instants its Activities began, and the instants a terminal state closed a run. A
// store error degrades to no events rather than failing the caller's resource.
func runActivityEvents(evs *hubstore.Events, root, ticket string) ([]activityStart, []time.Time) {
	acts, err := evs.Query(root, hubstore.EventFilter{Kind: "activity_change", Ticket: ticket, Limit: maxDurationEvents})
	if err != nil {
		logger.Verbosef("run activities %s/%s: %v", root, ticket, err)
		return nil, nil
	}
	if len(acts) == 0 {
		return nil, nil
	}
	states, err := evs.Query(root, hubstore.EventFilter{Kind: "state_change", Ticket: ticket, Limit: maxDurationEvents})
	if err != nil {
		logger.Verbosef("run activities %s/%s: %v", root, ticket, err)
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

	return starts, terminals
}

// currentRun narrows a ticket's Activity starts to its latest run and reports the
// instant that run closed, so totals track the run's displayed wall-clock even
// after a pause and resume. The current run is the span of activity_change events
// after the terminal state_change that precedes the most recent one, closed by the
// terminal state_change that follows it — or, for a run still in flight, left open
// at its last Activity (which then contributes nothing until the next change).
func currentRun(starts []activityStart, terminals []time.Time) ([]activityStart, time.Time) {
	if len(starts) == 0 {
		return nil, time.Time{}
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

	current := make([]activityStart, 0, len(starts))
	for _, a := range starts {
		if a.at.After(prior) && !a.at.After(end) {
			current = append(current, a)
		}
	}
	return current, end
}

// stepDurations groups the derived per-Activity spans into their display Steps.
func stepDurations(starts []activityStart, terminals []time.Time) []StepDuration {
	current, end := currentRun(starts, terminals)
	byStep := make(map[activity.Step]time.Duration, len(activity.Steps))
	for _, span := range spansOf(current, end) {
		byStep[activity.StepOf(span.act)] += span.d
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

// activitySpans sums the run's spans per Activity, in the order each Activity first
// ran, so a self-heal loop that re-entered verify reads as one line rather than
// several.
func activitySpans(current []activityStart, end time.Time) []ActivitySpan {
	byActivity := map[activity.Activity]time.Duration{}
	order := make([]activity.Activity, 0, len(current))
	for _, span := range spansOf(current, end) {
		if _, seen := byActivity[span.act]; !seen {
			order = append(order, span.act)
		}
		byActivity[span.act] += span.d
	}

	out := make([]ActivitySpan, 0, len(order))
	for _, act := range order {
		out = append(out, ActivitySpan{Activity: string(act), DurationMS: byActivity[act].Milliseconds()})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// spansOf closes each Activity in a run against the next one, and the last against
// the run's end. A span with no elapsed time is dropped rather than reported as a
// zero.
func spansOf(current []activityStart, end time.Time) []activitySpan {
	out := make([]activitySpan, 0, len(current))
	for i, a := range current {
		next := end
		if i+1 < len(current) {
			next = current[i+1].at
		}
		if d := next.Sub(a.at); d > 0 {
			out = append(out, activitySpan{act: a.act, d: d})
		}
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
