package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

// Issues is the hub's authoritative issue store: one table holding both synced
// tracker tickets and internally-created issues, each row carrying a source
// binding (internal | linear | jira). It is the working copy trau reads (ADR
// 0007) — description and comments included — populated by the hub's inbound sync.
// The store is global; every row is scoped to a repo root.
type Issues struct {
	db *sql.DB
}

// NewIssues returns the issue store over db. The caller owns db's lifecycle.
func NewIssues(db *sql.DB) *Issues { return &Issues{db: db} }

// Issue is one stored issue row with its comments. Labels is a decoded name list.
type Issue struct {
	Repo        string
	Source      string
	Identifier  string
	Title       string
	Description string
	Status      string
	StatusGroup string
	Priority    int
	Labels      []string
	Parent      string
	HasChildren bool
	DueDate     string
	ExternalID  string
	URL         string
	CreatedAt   string
	UpdatedAt   string
	Comments    []Comment
}

// Comment is one comment on an issue, keyed by its external tracker id.
type Comment struct {
	ExternalID string
	Author     string
	Body       string
	CreatedAt  string
	UpdatedAt  string
}

// SyncBinding is a repo's resolved tracker target — the stable ids a sync pull
// filters on — cached so later syncs skip the team/project lookup.
type SyncBinding struct {
	TeamID    string
	ProjectID string
	Project   string
}

// SyncState is a repo's sync bookkeeping: the cached binding, the last cursor,
// and the outcome of the last sync.
type SyncState struct {
	Binding      SyncBinding
	Cursor       string
	LastSyncedAt string
	LastIssues   int
	LastComments int
	LastError    string
}

// SyncResult records the outcome of one sync on the bookkeeping row.
type SyncResult struct {
	Issues   int
	Comments int
	Cursor   string
	SyncedAt string
	Err      string
}

// Upsert idempotently writes issues and their comments for a repo under one
// transaction: issues by (repo, identifier), comments by (issue_id, external_id),
// so re-running a sync updates in place rather than duplicating. Issues missing
// from a later pull are left intact — deletion reconciliation is a separate slice.
// An identifier already held by an internal issue is never overwritten: inbound
// sync only ever writes tracker content, so the conflict update skips a
// source=internal row (ADR 0007). It returns the number of issues and comments
// written.
func (s *Issues) Upsert(repo, source string, issues []Issue) (issueCount, commentCount int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	syncedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, iss := range issues {
		labels, err := json.Marshal(labelList(iss.Labels))
		if err != nil {
			return 0, 0, errors.Join(err, tx.Rollback())
		}
		var id int64
		err = tx.QueryRow(
			`INSERT INTO issues(
				repo, source, identifier, title, description, status, status_group,
				priority, labels, parent, has_children, due_date, external_id, url,
				created_at, updated_at, synced_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(repo, identifier) DO UPDATE SET
				source = excluded.source, title = excluded.title,
				description = excluded.description, status = excluded.status,
				status_group = excluded.status_group, priority = excluded.priority,
				labels = excluded.labels, parent = excluded.parent,
				has_children = excluded.has_children, due_date = excluded.due_date,
				external_id = excluded.external_id, url = excluded.url,
				created_at = excluded.created_at, updated_at = excluded.updated_at,
				synced_at = excluded.synced_at
			 WHERE issues.source <> 'internal'
			 RETURNING id`,
			repo, source, iss.Identifier, iss.Title, iss.Description, iss.Status,
			iss.StatusGroup, iss.Priority, string(labels), iss.Parent,
			boolToInt(iss.HasChildren), iss.DueDate, iss.ExternalID, iss.URL,
			iss.CreatedAt, iss.UpdatedAt, syncedAt,
		).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, 0, errors.Join(err, tx.Rollback())
		}
		for _, c := range iss.Comments {
			if _, err := tx.Exec(
				`INSERT INTO issue_comments(issue_id, external_id, author, body, created_at, updated_at)
				 VALUES(?, ?, ?, ?, ?, ?)
				 ON CONFLICT(issue_id, external_id) DO UPDATE SET
					author = excluded.author, body = excluded.body,
					created_at = excluded.created_at, updated_at = excluded.updated_at`,
				id, c.ExternalID, c.Author, c.Body, c.CreatedAt, c.UpdatedAt,
			); err != nil {
				return 0, 0, errors.Join(err, tx.Rollback())
			}
			commentCount++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(issues), commentCount, nil
}

const issueColumns = `id, source, identifier, title, description, status, status_group,
	priority, labels, parent, has_children, due_date, external_id, url,
	created_at, updated_at`

// List returns a repo's stored issues with their comments, ordered by identifier.
func (s *Issues) List(repo string) (issues []Issue, err error) {
	rows, err := s.db.Query(
		`SELECT `+issueColumns+` FROM issues WHERE repo = ? ORDER BY identifier`,
		repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, ids, err := scanIssues(repo, rows)
	if err != nil {
		return nil, err
	}
	return s.attachComments(repo, issues, ids)
}

// BacklogFilter narrows a backlog listing. Group matches the workflow state group
// (backlog | unstarted | started | completed | canceled); Label matches an issue
// carrying that label name, case-insensitively; Source is "internal" for
// internally-created issues or "synced" for tracker tickets; Text is a
// case-insensitive substring over identifier and title. A zero-valued field is
// ignored, so the zero filter selects the whole board. Limit and Offset paginate
// the ordered matches; a Limit of zero returns every match.
type BacklogFilter struct {
	Group  string
	Label  string
	Source string
	Text   string
	Limit  int
	Offset int
}

// Backlog returns a repo's stored issues for the board, ordered by identifier and
// without comments — the whole board, equivalent to an empty BacklogFilter.
func (s *Issues) Backlog(repo string) ([]Issue, error) {
	issues, _, err := s.BacklogPage(repo, BacklogFilter{})
	return issues, err
}

// BacklogPage returns the repo's stored issues matching filter, ordered by
// identifier and paginated, together with the total number of matches before
// pagination so the board can page without counting the rows itself. The filters
// compose in the WHERE clause and are pushed into the query rather than applied
// after loading everything; comments are not attached (the board renders summary
// rows only).
func (s *Issues) BacklogPage(repo string, filter BacklogFilter) (issues []Issue, total int, err error) {
	where := []string{"repo = ?"}
	args := []any{repo}
	if group := strings.TrimSpace(filter.Group); group != "" {
		where = append(where, "status_group = ?")
		args = append(args, group)
	}
	switch strings.TrimSpace(filter.Source) {
	case "internal":
		where = append(where, "source = 'internal'")
	case "synced":
		where = append(where, "source <> 'internal'")
	}
	if label := strings.TrimSpace(filter.Label); label != "" {
		where = append(where, "EXISTS (SELECT 1 FROM json_each(labels) WHERE lower(value) = lower(?))")
		args = append(args, label)
	}
	if text := strings.TrimSpace(filter.Text); text != "" {
		like := "%" + escapeLike(text) + "%"
		where = append(where, `(identifier LIKE ? ESCAPE '\' OR title LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	clause := strings.Join(where, " AND ")

	if err = s.db.QueryRow(`SELECT count(*) FROM issues WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + issueColumns + ` FROM issues WHERE ` + clause + ` ORDER BY identifier`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
		if filter.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, filter.Offset)
		}
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, err = scanIssues(repo, rows)
	return issues, total, err
}

// escapeLike escapes the SQLite LIKE metacharacters so a filter term matches
// literally — a user typing "%" or "_" narrows the board instead of matching
// everything.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// scanIssues reads issue rows selected in issueColumns order into a slice and an
// id→index map its caller can join comments onto.
func scanIssues(repo string, rows *sql.Rows) ([]Issue, map[int64]int, error) {
	issues := []Issue{}
	ids := map[int64]int{}
	for rows.Next() {
		var (
			id     int64
			labels string
			hasCh  int
			iss    = Issue{Repo: repo}
		)
		if err := rows.Scan(
			&id, &iss.Source, &iss.Identifier, &iss.Title, &iss.Description,
			&iss.Status, &iss.StatusGroup, &iss.Priority, &labels, &iss.Parent,
			&hasCh, &iss.DueDate, &iss.ExternalID, &iss.URL, &iss.CreatedAt, &iss.UpdatedAt,
		); err != nil {
			return nil, nil, err
		}
		iss.HasChildren = hasCh != 0
		iss.Labels = decodeLabels(labels)
		iss.Comments = []Comment{}
		ids[id] = len(issues)
		issues = append(issues, iss)
	}
	return issues, ids, rows.Err()
}

func (s *Issues) attachComments(repo string, issues []Issue, byID map[int64]int) (_ []Issue, err error) {
	rows, err := s.db.Query(
		`SELECT c.issue_id, c.external_id, c.author, c.body, c.created_at, c.updated_at
		 FROM issue_comments c JOIN issues i ON i.id = c.issue_id
		 WHERE i.repo = ? ORDER BY c.id`,
		repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var (
			issueID int64
			c       Comment
		)
		if scanErr := rows.Scan(&issueID, &c.ExternalID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt); scanErr != nil {
			return nil, scanErr
		}
		if idx, ok := byID[issueID]; ok {
			issues[idx].Comments = append(issues[idx].Comments, c)
		}
	}
	return issues, rows.Err()
}

// defaultSearchLimit caps how many ranked matches a single search returns; the
// entry point is a type-ahead, not a full listing.
const defaultSearchLimit = 20

// Search returns a repo's issues matching query, ranked best-first over the FTS5
// index of identifier, title, description, and labels. Identifier and title
// outweigh description so a hit in either surfaces above a body-only mention. A
// query that reduces to no searchable tokens — blank, or all punctuation —
// returns no matches rather than an error, so the caller need not pre-validate.
func (s *Issues) Search(repo, query string, limit int) (issues []Issue, err error) {
	match := buildMatchQuery(query)
	if match == "" {
		return []Issue{}, nil
	}
	if limit <= 0 || limit > defaultSearchLimit {
		limit = defaultSearchLimit
	}
	rows, err := s.db.Query(
		`SELECT `+prefixColumns("i")+`
		 FROM issues_fts f JOIN issues i ON i.id = f.rowid
		 WHERE issues_fts MATCH ? AND i.repo = ?
		 ORDER BY bm25(issues_fts, 10.0, 5.0, 1.0, 3.0)
		 LIMIT ?`,
		match, repo, limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, err = scanIssues(repo, rows)
	return issues, err
}

// buildMatchQuery turns free-form user input into a safe FTS5 MATCH expression:
// each run of letters and digits becomes a quoted prefix term, ANDed together.
// Quoting neutralizes every FTS5 operator, so quotes, hyphens, and ticket-style
// ids like ABC-123 (two terms, "abc" and "123") never trip the query parser.
func buildMatchQuery(query string) string {
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if len(terms) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range terms {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(t)
		b.WriteString(`"*`)
	}
	return b.String()
}

func prefixColumns(alias string) string {
	cols := strings.Split(issueColumns, ",")
	for i, c := range cols {
		cols[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

// SyncState returns a repo's sync bookkeeping, zero-valued when it has never
// synced.
func (s *Issues) SyncState(repo string) (SyncState, error) {
	var st SyncState
	err := s.db.QueryRow(
		`SELECT team_id, project_id, project, cursor, last_synced_at, last_issues, last_comments, last_error
		 FROM issue_sync WHERE repo = ?`,
		repo,
	).Scan(
		&st.Binding.TeamID, &st.Binding.ProjectID, &st.Binding.Project, &st.Cursor,
		&st.LastSyncedAt, &st.LastIssues, &st.LastComments, &st.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{}, nil
	}
	if err != nil {
		return SyncState{}, err
	}
	return st, nil
}

// SaveBinding caches a repo's resolved team/project ids so later syncs reuse them
// instead of re-resolving through a team list round-trip.
func (s *Issues) SaveBinding(repo string, b SyncBinding) error {
	_, err := s.db.Exec(
		`INSERT INTO issue_sync(repo, team_id, project_id, project) VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET
			team_id = excluded.team_id, project_id = excluded.project_id, project = excluded.project`,
		repo, b.TeamID, b.ProjectID, b.Project,
	)
	return err
}

// RecordResult stores the outcome of a sync — counts, cursor, timestamp, and any
// error — on the repo's bookkeeping row.
func (s *Issues) RecordResult(repo string, r SyncResult) error {
	_, err := s.db.Exec(
		`INSERT INTO issue_sync(repo, cursor, last_synced_at, last_issues, last_comments, last_error)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET
			cursor = excluded.cursor, last_synced_at = excluded.last_synced_at,
			last_issues = excluded.last_issues, last_comments = excluded.last_comments,
			last_error = excluded.last_error`,
		repo, r.Cursor, r.SyncedAt, r.Issues, r.Comments, r.Err,
	)
	return err
}

// RecordError stamps a failed sync's error on the repo's bookkeeping row without
// touching the cursor, counts, or last-synced time, so a transient tracker
// failure leaves the last good sync — and its incremental cursor — intact. A
// later successful RecordResult clears the error.
func (s *Issues) RecordError(repo, msg string) error {
	_, err := s.db.Exec(
		`INSERT INTO issue_sync(repo, last_error) VALUES(?, ?)
		 ON CONFLICT(repo) DO UPDATE SET last_error = excluded.last_error`,
		repo, msg,
	)
	return err
}

func labelList(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}

func decodeLabels(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	return out
}
