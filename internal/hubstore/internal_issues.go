package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SourceInternal marks an issue that lives only in the hub store — created and
// edited inside trau, never pushed to any external tracker (ADR 0007).
const SourceInternal = "internal"

// ErrInternalIssueNotFound is returned when an update targets an identifier that
// is not an existing internal issue (absent, or a synced ticket whose content
// the store must not edit).
var ErrInternalIssueNotFound = errors.New("internal issue not found")

// InternalDraft is the editable content of an internal issue: its title,
// markdown description, workflow state (a status group — see normalizeState),
// labels, and an optional parent identifier nesting it under an epic.
type InternalDraft struct {
	Title       string
	Description string
	State       string
	Labels      []string
	Parent      string
}

// CreateInternal files a new internal issue for a repo, allocating the next
// identifier from the repo's sequence prefixed with prefix (e.g. LOOP-12) and
// writing it with source "internal". The identifier is unique within the repo:
// the sequence advances past any number an existing issue — internal or synced —
// already holds, and the whole allocation runs in one transaction so concurrent
// creates never collide. A blank prefix or title is rejected.
func (s *Issues) CreateInternal(repo, prefix string, d InternalDraft) (Issue, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		return Issue{}, errors.New("issue prefix is empty")
	}
	title := strings.TrimSpace(d.Title)
	if title == "" {
		return Issue{}, errors.New("issue title is empty")
	}
	group, status := normalizeState(d.State)
	parent := strings.TrimSpace(d.Parent)
	labels := labelList(d.Labels)
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Issue{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Issue{}, err
	}
	if _, err := tx.Exec(`INSERT INTO issue_seq(repo, next) VALUES(?, 1) ON CONFLICT(repo) DO NOTHING`, repo); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	var next int64
	if err := tx.QueryRow(`SELECT next FROM issue_seq WHERE repo = ?`, repo).Scan(&next); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	identifier, err := freeIdentifier(tx, repo, prefix, &next)
	if err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`INSERT INTO issues(repo, source, identifier, title, description, status, status_group, labels, parent, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repo, SourceInternal, identifier, title, d.Description, status, group, string(labelsJSON), parent, now, now,
	); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(`UPDATE issue_seq SET next = ? WHERE repo = ?`, next+1, repo); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	if err := markParent(tx, repo, parent); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	return Issue{
		Repo: repo, Source: SourceInternal, Identifier: identifier,
		Title: title, Description: d.Description, Status: status, StatusGroup: group,
		Labels: labels, Parent: parent, CreatedAt: now, UpdatedAt: now, Comments: []Comment{},
	}, nil
}

// UpdateInternal replaces the editable fields of an existing internal issue. It
// only ever touches a source=internal row — a missing or synced identifier
// yields ErrInternalIssueNotFound, so tracker content is never edited through
// this path.
func (s *Issues) UpdateInternal(repo, identifier string, d InternalDraft) (Issue, error) {
	title := strings.TrimSpace(d.Title)
	if title == "" {
		return Issue{}, errors.New("issue title is empty")
	}
	group, status := normalizeState(d.State)
	parent := strings.TrimSpace(d.Parent)
	labels := labelList(d.Labels)
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Issue{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.db.Begin()
	if err != nil {
		return Issue{}, err
	}
	res, err := tx.Exec(
		`UPDATE issues SET title = ?, description = ?, status = ?, status_group = ?, labels = ?, parent = ?, updated_at = ?
		 WHERE repo = ? AND identifier = ? AND source = ?`,
		title, d.Description, status, group, string(labelsJSON), parent, now, repo, identifier, SourceInternal,
	)
	if err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	if n == 0 {
		return Issue{}, errors.Join(ErrInternalIssueNotFound, tx.Rollback())
	}
	if err := markParent(tx, repo, parent); err != nil {
		return Issue{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	return Issue{
		Repo: repo, Source: SourceInternal, Identifier: identifier,
		Title: title, Description: d.Description, Status: status, StatusGroup: group,
		Labels: labels, Parent: parent, UpdatedAt: now, Comments: []Comment{},
	}, nil
}

// Internal returns a single internal issue by identifier, reporting found=false
// when no internal issue owns it. Synced tickets are invisible here — this is the
// getter the edit form reads, and only internal issues are editable.
func (s *Issues) Internal(repo, identifier string) (iss Issue, found bool, err error) {
	rows, err := s.db.Query(
		`SELECT `+issueColumns+` FROM issues WHERE repo = ? AND identifier = ? AND source = ?`,
		repo, identifier, SourceInternal,
	)
	if err != nil {
		return Issue{}, false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, scanErr := scanIssues(repo, rows)
	if scanErr != nil {
		return Issue{}, false, scanErr
	}
	if len(issues) == 0 {
		return Issue{}, false, nil
	}
	return issues[0], true, nil
}

// InternalChildren returns a repo's internal issues nested under parent, ordered
// by identifier — the sub-issues a queued internal epic carries.
func (s *Issues) InternalChildren(repo, parent string) (issues []Issue, err error) {
	rows, err := s.db.Query(
		`SELECT `+issueColumns+` FROM issues WHERE repo = ? AND parent = ? AND source = ? ORDER BY identifier`,
		repo, parent, SourceInternal,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, err = scanIssues(repo, rows)
	return issues, err
}

// freeIdentifier returns the first prefix-N identifier not already present in the
// repo, advancing *next past any taken number so a synced ticket that happens to
// share the prefix can never shadow an internal one.
func freeIdentifier(tx *sql.Tx, repo, prefix string, next *int64) (string, error) {
	for {
		id := fmt.Sprintf("%s-%d", prefix, *next)
		var one int
		err := tx.QueryRow(`SELECT 1 FROM issues WHERE repo = ? AND identifier = ?`, repo, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		*next++
	}
}

// markParent flags parent as having children so the board renders it as an epic
// once a sub-issue is nested under it. A blank parent is a no-op.
func markParent(tx *sql.Tx, repo, parent string) error {
	if parent == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE issues SET has_children = 1 WHERE repo = ? AND identifier = ?`, repo, parent)
	return err
}

// internalStates maps a normalized workflow state group to the display status
// stored alongside it, so an internal issue reads consistently on the board.
var internalStates = map[string]string{
	"backlog":   "Backlog",
	"unstarted": "Todo",
	"started":   "In Progress",
	"done":      "Done",
	"canceled":  "Canceled",
}

// normalizeState resolves a requested state onto its stored status group and
// display status, defaulting an empty or unknown value to backlog so an internal
// issue always carries a valid, board-renderable state.
func normalizeState(state string) (group, status string) {
	group = strings.ToLower(strings.TrimSpace(state))
	display, ok := internalStates[group]
	if !ok {
		return "backlog", internalStates["backlog"]
	}
	return group, display
}
