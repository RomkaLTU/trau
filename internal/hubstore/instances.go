package hubstore

import (
	"database/sql"
	"sort"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
)

// Instances is the hub's authoritative record of the live loops on this machine
// (ADR 0005, ADR 0008 §7). Each loop upserts its presence over HTTP on start, on
// every session-state change, and on a heartbeat timer; the row carries the
// reported session state the hub echoes verbatim. Liveness stays pid-only: Live
// probes each row's PID with signal 0 and reaps the ones whose process is gone,
// so a crashed child that never deregistered ages out. Heartbeat staleness never
// reaps a live PID — a suspended loop keeps its repo-is-live guard.
type Instances struct {
	db    *sql.DB
	alive func(pid int) bool
}

// NewInstances returns an Instances store over db. The caller owns db's lifecycle.
func NewInstances(db *sql.DB) *Instances {
	return &Instances{db: db, alive: registry.Alive}
}

// Upsert records or refreshes a loop's presence, keyed by PID.
func (in *Instances) Upsert(e registry.Entry) error {
	_, err := in.db.Exec(
		`INSERT INTO instances(pid, repo_root, runs_dir, started_at, heartbeat, session_state, ticket, phase, activity, detail, state_since)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(pid) DO UPDATE SET
		   repo_root = excluded.repo_root,
		   runs_dir = excluded.runs_dir,
		   started_at = excluded.started_at,
		   heartbeat = excluded.heartbeat,
		   session_state = excluded.session_state,
		   ticket = excluded.ticket,
		   phase = excluded.phase,
		   activity = excluded.activity,
		   detail = excluded.detail,
		   state_since = excluded.state_since`,
		e.PID, e.RepoRoot, e.RunsDir,
		formatInstanceTime(e.StartedAt), formatInstanceTime(e.Heartbeat),
		e.SessionState, e.Ticket, e.Phase, e.Activity, e.Detail, formatInstanceTime(e.StateSince),
	)
	return err
}

// Remove drops a loop's presence — the deregister on clean exit. Idempotent: a
// PID with no row is not an error.
func (in *Instances) Remove(pid int) error {
	_, err := in.db.Exec(`DELETE FROM instances WHERE pid = ?`, pid)
	return err
}

// Live returns the loops whose process is still alive, oldest first, reaping the
// rows of any whose PID no longer names a running process. It is the hub's read
// side of presence.
func (in *Instances) Live() ([]registry.Entry, error) {
	rows, err := in.db.Query(
		`SELECT pid, repo_root, runs_dir, started_at, heartbeat, session_state, ticket, phase, activity, detail, state_since FROM instances`)
	if err != nil {
		return nil, err
	}
	var entries []registry.Entry
	for rows.Next() {
		var (
			e                              registry.Entry
			started, heartbeat, stateSince string
		)
		if err := rows.Scan(&e.PID, &e.RepoRoot, &e.RunsDir, &started, &heartbeat, &e.SessionState, &e.Ticket, &e.Phase, &e.Activity, &e.Detail, &stateSince); err != nil {
			_ = rows.Close()
			return nil, err
		}
		e.StartedAt = parseInstanceTime(started)
		e.Heartbeat = parseInstanceTime(heartbeat)
		e.StateSince = parseInstanceTime(stateSince)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	live := make([]registry.Entry, 0, len(entries))
	for _, e := range entries {
		if in.alive(e.PID) {
			live = append(live, e)
			continue
		}
		_, _ = in.db.Exec(`DELETE FROM instances WHERE pid = ?`, e.PID)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].StartedAt.Before(live[j].StartedAt) })
	return live, nil
}

func formatInstanceTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseInstanceTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
