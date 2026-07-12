// Package event is the structured-logging foundation for the Trau loop.
//
// Every significant action — each agent invocation, every phase transition —
// emits exactly one [Event] as a single JSON line onto a [Log]. Keeping the
// emission point here means "one event per action" is enforced in one place.
package event

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// KindAgentStart marks the start of an agent run, carrying its live transcript path.
const KindAgentStart = "agent_start"

// KindBuildNoSkills marks a build that loaded no skills in a repo that has skills
// installed — the agent skipped the skills the repo expected it to use.
const KindBuildNoSkills = "build_no_skills"

// KindSpawnFailed marks a hub-spawned child that exited before it could register
// or write a checkpoint — dead on arrival. It carries the child's exit code and a
// tail of its stderr so the failure is diagnosable from the web UI.
const KindSpawnFailed = "spawn_failed"

// Event is one structured log record. Fields carries action-specific detail
// (token counts, ids, durations) so the schema can grow without churning the
// envelope.
type Event struct {
	Time   string         `json:"ts"`
	Kind   string         `json:"kind"`
	Phase  string         `json:"phase,omitempty"`
	Msg    string         `json:"msg,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// Sink receives each event as a structured record — the durable destination that
// wants the event itself rather than its JSON line. The loop child sends events to
// the hub through a Sink (ADR 0008) instead of appending them to a log file.
type Sink interface {
	Event(Event)
}

// Log fans each emitted event out to its configured destinations: an optional
// JSON-line writer (the --json diagnostic stream), an optional structured Sink
// (the hub), and an optional human renderer. It is safe for concurrent use: phases
// and control actions stream from separate goroutines onto the same Log.
type Log struct {
	mu    sync.Mutex
	w     io.Writer
	sink  Sink
	human func(Event)
	now   func() time.Time
}

// New returns a Log that writes JSON lines to w using the wall clock.
func New(w io.Writer) *Log {
	return &Log{w: w, now: time.Now}
}

// NewSink returns a Log that sends each event to s as a structured record, using
// the wall clock. It writes no JSON lines of its own.
func NewSink(s Sink) *Log {
	return &Log{sink: s, now: time.Now}
}

// WithClock overrides the timestamp source; intended for deterministic tests.
func (l *Log) WithClock(now func() time.Time) *Log {
	l.now = now
	return l
}

// WithHuman attaches a renderer called once per Emit (after the JSON line is
// written) so a human-facing surface — internal/console — can display the event
// without re-reading the file. The JSON stream stays the durable source of truth;
// this is display only. nil (the default) means JSON-only.
func (l *Log) WithHuman(fn func(Event)) *Log {
	l.human = fn
	return l
}

// Emit dispatches a single event to every configured destination. A marshal or
// write error is dropped on purpose: logging must never abort the loop. fields may
// be nil.
func (l *Log) Emit(kind, phase, msg string, fields map[string]any) {
	ev := Event{
		Time:   l.now().Format(time.RFC3339),
		Kind:   kind,
		Phase:  phase,
		Msg:    msg,
		Fields: fields,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w != nil {
		if line, err := json.Marshal(ev); err == nil {
			_, _ = l.w.Write(append(line, '\n'))
		}
	}
	if l.sink != nil {
		l.sink.Event(ev)
	}
	if l.human != nil {
		l.human(ev)
	}
}
