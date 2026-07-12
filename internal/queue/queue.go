// Package queue is the shared vocabulary of the per-Repo execution queue — the
// item, sub-issue, status, on-fault, and drain-outcome types the hub and its
// children both speak. The queue itself, its order and drain state, lives in the
// hub database (internal/hubstore, ADR 0007); only the `trau serve` process opens
// it. The loop child never touches the queue: it posts its exit outcome to the
// hub over HTTP (ADR 0008), so this package stays free of any database dependency.
package queue

import (
	"errors"
	"time"
)

// Kind distinguishes a single run-once ticket from an epic carrying its
// sub-issues.
type Kind string

const (
	KindTicket Kind = "ticket"
	KindEpic   Kind = "epic"
)

// The statuses an item moves through as the hub drains the queue: registration
// lands it Pending, draining marks it Running, and the child's outcome settles
// it Done, Failed, or — when the run faults or a provider pauses — Paused, parked
// at the front for a resume to re-attempt.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusPaused  = "paused"
	StatusDone    = "done"
	StatusFailed  = "failed"
	// StatusSkipped marks an item the drain passed over without running: a
	// duplicate of work already claimed elsewhere in the same queue.
	StatusSkipped = "skipped"
)

// OnFault selects what a fault does to the rest of the queue: halt parks the
// item and stops the drain for a human, skip settles it failed and continues.
const (
	OnFaultHalt = "halt"
	OnFaultSkip = "skip"
)

// SubIssue is one child an epic item carries, captured when the epic is queued
// so the queue records what an epic run will cover.
type SubIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// Item is one queued unit of work — a run-once ticket or an epic. Its position
// is implicit in the queue's order. PID is the child the hub spawned to run it,
// set while Running so a resumed hub can tell whether that child is still alive.
type Item struct {
	Kind      Kind       `json:"kind"`
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Status    string     `json:"status"`
	Reason    string     `json:"reason,omitempty"`
	PID       int        `json:"pid,omitempty"`
	SubIssues []SubIssue `json:"sub_issues,omitempty"`
	QueuedAt  time.Time  `json:"queued_at"`
}

// Meta is the queue's run-level configuration, read alongside its items to drive
// the drain: whether to ignore stored checkpoints and what a fault does.
type Meta struct {
	Draining bool
	NoResume bool
	OnFault  string
}

var (
	// ErrAlreadyQueued is returned when the same ticket or epic is registered
	// twice, so work is never queued more than once.
	ErrAlreadyQueued = errors.New("already in the queue")
	// ErrNotQueued is returned when removing an item the queue does not hold.
	ErrNotQueued = errors.New("not in the queue")
	// ErrRunning is returned when removing an item the hub is currently
	// draining, so a running child is never orphaned by a dequeue.
	ErrRunning = errors.New("cannot remove a running item")
)

// DrainReport is how a headless queue-member child reports to the hub drainer how
// it exited: when the run ended on a fault or provider pause, the failure class
// and reason; both empty for a clean finish. It lets the drain settle an item —
// including an epic whose fault lives on a sub-issue's checkpoint, not the epic's
// — from the child's own outcome rather than the epic checkpoint, which never
// shows it.
type DrainReport struct {
	Class  string `json:"class,omitempty"`
	Reason string `json:"reason,omitempty"`
}
