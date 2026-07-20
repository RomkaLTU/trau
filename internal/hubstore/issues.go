package hubstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	Repo         string
	Source       string
	Identifier   string
	Title        string
	Description  string
	Status       string
	StatusGroup  string
	Priority     int
	Labels       []string
	Parent       string
	HasChildren  bool
	DueDate      string
	ExternalID   string
	URL          string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
	ArchivedAt   string
	AssigneeID   string
	AssigneeName string
	Comments     []Comment

	// ChildrenSettled and ChildrenTotal are populated by BacklogPage for epic
	// rows only (HasChildren): the epic's settled (done + canceled) and total
	// sub-issue counts over every child in the store, not the requested page.
	ChildrenSettled int
	ChildrenTotal   int
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

// SyncIdentity is the repo binding's resolved Me — the tracker user behind its
// credentials — with the time it was last resolved. A zero value means the repo's
// identity has not been resolved yet.
type SyncIdentity struct {
	ID         string
	Name       string
	ResolvedAt string
}

// SyncState is a repo's sync bookkeeping: the cached binding, the last cursor,
// the outcome of the last sync, and the resolved Me identity.
type SyncState struct {
	Binding      SyncBinding
	Cursor       string
	LastSyncedAt string
	LastIssues   int
	LastComments int
	LastError    string
	Me           SyncIdentity
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
// from a later pull are left intact; a previously tombstoned issue that a pull
// returns again is revived (its deleted_at cleared), so an issue moved back into
// the Project un-tombstones on the next sync. An identifier already held by an
// internal issue is never overwritten: inbound
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
				created_at, updated_at, synced_at, assignee_id, assignee_name)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))
			 ON CONFLICT(repo, identifier) DO UPDATE SET
				source = excluded.source, title = excluded.title,
				description = excluded.description, status = excluded.status,
				status_group = excluded.status_group, priority = excluded.priority,
				labels = excluded.labels, parent = excluded.parent,
				has_children = excluded.has_children, due_date = excluded.due_date,
				external_id = excluded.external_id, url = excluded.url,
				created_at = excluded.created_at, updated_at = excluded.updated_at,
				synced_at = excluded.synced_at, deleted_at = '',
				assignee_id = excluded.assignee_id, assignee_name = excluded.assignee_name
			 WHERE issues.source <> 'internal'
			 RETURNING id`,
			repo, source, iss.Identifier, iss.Title, iss.Description, iss.Status,
			iss.StatusGroup, iss.Priority, string(labels), iss.Parent,
			boolToInt(iss.HasChildren), iss.DueDate, iss.ExternalID, iss.URL,
			iss.CreatedAt, iss.UpdatedAt, syncedAt, iss.AssigneeID, iss.AssigneeName,
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
	created_at, updated_at, deleted_at, archived_at, assignee_id, assignee_name`

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

// Children returns a repo's issues nested under parent across every source,
// ordered by identifier — the sub-issues created under an epic. A blank parent
// returns nothing. Comments are not attached; callers key on identifier and title.
func (s *Issues) Children(repo, parent string) (issues []Issue, err error) {
	if strings.TrimSpace(parent) == "" {
		return []Issue{}, nil
	}
	rows, err := s.db.Query(
		`SELECT `+issueColumns+` FROM issues WHERE repo = ? AND parent = ? ORDER BY identifier`,
		repo, parent,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, err = scanIssues(repo, rows)
	return issues, err
}

// BacklogFilter narrows a backlog listing. Groups matches the workflow state
// groups to union (backlog | unstarted | started | done | canceled | unknown);
// Label matches an issue carrying that label name, case-insensitively; Source is
// "internal" for internally-created issues or "synced" for tracker tickets;
// Assignee is "me" (the repo binding's resolved identity), "unassigned", or an
// assignee id to match exactly — an unresolved "me" matches nothing; Text is a
// case-insensitive substring over identifier and title; Parent matches the
// direct sub-issues of that epic identifier. Archived selects the view: false
// hides every archived family (a row whose own archived_at is set, or whose
// family keys onto an archived identifier), true shows only those. A zero-valued
// field is ignored, so the zero filter selects the whole live board — an empty
// Groups means every group. Limit and Offset paginate the ordered matches; a
// Limit of zero returns every match.
type BacklogFilter struct {
	Groups   []string
	Label    string
	Source   string
	Assignee string
	Text     string
	Parent   string
	Archived bool
	Limit    int
	Offset   int
}

// familyKey groups a row with its epic: a sub-issue keys on its parent, a
// top-level issue on its own identifier. parent is NOT NULL DEFAULT ” in the
// schema, so NULLIF collapses the empty top-level case to the row's identifier.
const familyKey = `COALESCE(NULLIF(parent, ''), identifier)`

// archivedFamily matches a row that belongs to an archived family: its own
// archived_at is stamped, or its family key is an archived identifier — so a
// child vanishes with the epic it hangs off, including one synced after the epic
// was archived. It carries one repo placeholder for the identifier subquery.
const archivedFamily = `(archived_at <> '' OR ` + familyKey +
	` IN (SELECT identifier FROM issues WHERE repo = ? AND archived_at <> ''))`

// numericIdentOrder renders the ORDER BY terms that sort an identifier expression
// numerically — the "COD-" prefix, then the trailing number as an integer, then
// the raw value — so COD-9 precedes COD-100 rather than sorting lexicographically.
func numericIdentOrder(expr string) string {
	return fmt.Sprintf(
		"substr(%[1]s, 1, instr(%[1]s, '-')), CAST(substr(%[1]s, instr(%[1]s, '-') + 1) AS INTEGER), %[1]s",
		expr,
	)
}

// familyCreated ranks a family by the newest created_at among its rows in the
// filtered set, so a fresh sub-issue surfaces its whole family together.
const familyCreated = `max(created_at) OVER (PARTITION BY ` + familyKey + `)`

// backlogGroup is the group a row files under on the board: an epic that is not
// yet closed surfaces as started while any live child is started, so the whole
// family reads as in progress, not just the sub-issue taken from it. Every other
// row keeps its stored status_group.
const backlogGroup = `CASE
	WHEN has_children = 1 AND status_group NOT IN ('started', 'done', 'canceled')
		AND EXISTS (
			SELECT 1 FROM issues c
			WHERE c.repo = issues.repo AND c.parent = issues.identifier
				AND c.deleted_at = '' AND c.status_group = 'started')
	THEN 'started'
	ELSE status_group
END`

// backlogColumns is issueColumns with status_group swapped for the board group,
// so backlog rows scan carrying the group they file under.
var backlogColumns = strings.Replace(issueColumns, "status_group", backlogGroup, 1)

// backlogOrderBy sorts the board by workflow progress — active work first, then
// not-yet-started, backlog, and finally the closed groups — keyed on the board
// group, so a promoted epic sorts with the started rows. The Todo and Backlog
// groups order families newest-created first, so a just-filed issue lands on top
// regardless of its identifier prefix; the other groups order by numeric-aware
// family key. Either way a family stays contiguous within a group: the epic ahead
// of its same-group sub-issues, then the rows' own numeric-aware identifiers.
var backlogOrderBy = `ORDER BY
	CASE ` + backlogGroup + `
		WHEN 'started' THEN 0
		WHEN 'unstarted' THEN 1
		WHEN 'backlog' THEN 2
		WHEN 'unknown' THEN 3
		WHEN 'done' THEN 4
		WHEN 'canceled' THEN 5
		ELSE 6
	END,
	CASE WHEN ` + backlogGroup + ` IN ('unstarted', 'backlog') THEN ` + familyCreated + ` END DESC,
	` + numericIdentOrder(familyKey) + `,
	CASE WHEN parent = '' THEN 0 ELSE 1 END,
	` + numericIdentOrder("identifier")

// Backlog returns a repo's stored issues for the board in display order and
// without comments — the whole board, equivalent to an empty BacklogFilter.
func (s *Issues) Backlog(repo string) ([]Issue, error) {
	issues, _, _, err := s.BacklogPage(repo, BacklogFilter{})
	return issues, err
}

// BacklogPage returns the repo's stored issues matching filter, ordered by group
// precedence (started, unstarted, backlog, unknown, done, canceled) then each
// group's display order (backlogOrderBy), and paginated. Grouping — the ordering,
// the state filter, the counts, and the rows' StatusGroup — is by the board group
// (backlogGroup), so an epic with a started child files under started as a whole.
// It also returns the total number of matches before pagination so the board can
// page without counting the rows itself, and per-board-group counts computed over
// the same filters with the state selection ignored — so section headers and the
// hidden-count hint hold whichever groups are on screen. Tombstoned issues —
// synced tickets removed from the tracker — are excluded from the board, as are
// archived families unless filter.Archived selects the archived view. The
// filters compose in the WHERE clause and are pushed into the query rather than
// applied after loading everything; comments are not attached (the board renders
// summary rows only).
func (s *Issues) BacklogPage(repo string, filter BacklogFilter) (issues []Issue, total int, counts map[string]int, err error) {
	where := []string{"repo = ?", "deleted_at = ''"}
	args := []any{repo}
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
		where = append(where, `(identifier LIKE ? ESCAPE '\' OR title LIKE ? ESCAPE '\' OR assignee_name LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	switch assignee := strings.TrimSpace(filter.Assignee); assignee {
	case "":
	case "me":
		// The repo's identity resolves to NULL when unset (no row, or me_id ''),
		// and assignee_id = NULL matches nothing — the null-identity degradation.
		where = append(where, "assignee_id = (SELECT NULLIF(me_id, '') FROM issue_sync WHERE repo = ?)")
		args = append(args, repo)
	case "unassigned":
		where = append(where, "assignee_id IS NULL")
	default:
		where = append(where, "assignee_id = ?")
		args = append(args, assignee)
	}
	if parent := strings.TrimSpace(filter.Parent); parent != "" {
		where = append(where, "parent = ?")
		args = append(args, parent)
	}
	if filter.Archived {
		where = append(where, archivedFamily)
	} else {
		where = append(where, "NOT "+archivedFamily)
	}
	args = append(args, repo)
	baseClause := strings.Join(where, " AND ")

	counts, err = s.backlogCounts(baseClause, args)
	if err != nil {
		return nil, 0, nil, err
	}

	clause := baseClause
	if groups := cleanGroups(filter.Groups); len(groups) > 0 {
		clause += " AND " + backlogGroup + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(groups)), ",") + ")"
		for _, g := range groups {
			args = append(args, g)
		}
	}

	if err = s.db.QueryRow(`SELECT count(*) FROM issues WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	query := `SELECT ` + backlogColumns + ` FROM issues WHERE ` + clause + ` ` + backlogOrderBy
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
		return nil, 0, nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	issues, _, err = scanIssues(repo, rows)
	if err != nil {
		return nil, 0, nil, err
	}
	if err = s.attachChildCounts(repo, issues); err != nil {
		return nil, 0, nil, err
	}
	return issues, total, counts, nil
}

// attachChildCounts fills ChildrenSettled/ChildrenTotal on the epic rows of a
// page. The counts cover every one of the epic's children still on the board —
// settled = done + canceled, the terminal groups the queue drain settles — so a
// collapsed epic's progress is whole even when the request's filters or
// pagination hide the children themselves. Individually-archived children are
// left out, so a visible epic's progress only counts work still in play.
func (s *Issues) attachChildCounts(repo string, issues []Issue) (err error) {
	byEpic := map[string]int{}
	epics := make([]any, 0, len(issues))
	for i, iss := range issues {
		if iss.HasChildren {
			byEpic[iss.Identifier] = i
			epics = append(epics, iss.Identifier)
		}
	}
	if len(epics) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(epics)), ",")
	args := append([]any{repo}, epics...)
	rows, err := s.db.Query(
		`SELECT parent, count(*),
			sum(CASE WHEN status_group IN ('done', 'canceled') THEN 1 ELSE 0 END)
		 FROM issues
		 WHERE repo = ? AND deleted_at = '' AND archived_at = '' AND parent IN (`+placeholders+`)
		 GROUP BY parent`,
		args...,
	)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var (
			parent         string
			total, settled int
		)
		if scanErr := rows.Scan(&parent, &total, &settled); scanErr != nil {
			return scanErr
		}
		if i, ok := byEpic[parent]; ok {
			issues[i].ChildrenTotal = total
			issues[i].ChildrenSettled = settled
		}
	}
	return rows.Err()
}

// backlogCounts returns per-board-group match totals for the given WHERE clause
// and its args — the board's non-state filters — with the state selection left
// out, so the section headers and the "N done · M canceled hidden" hint stay
// correct regardless of pagination or which groups are on screen.
func (s *Issues) backlogCounts(clause string, args []any) (counts map[string]int, err error) {
	rows, err := s.db.Query(`SELECT `+backlogGroup+` AS grp, count(*) FROM issues WHERE `+clause+` GROUP BY grp`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	counts = map[string]int{}
	for rows.Next() {
		var (
			group string
			n     int
		)
		if scanErr := rows.Scan(&group, &n); scanErr != nil {
			return nil, scanErr
		}
		counts[group] = n
	}
	return counts, rows.Err()
}

// LabelCount is one distinct label carried by a repo's issues and the number of
// issues carrying it.
type LabelCount struct {
	Name  string
	Count int
}

// Labels returns the distinct label names carried by a repo's stored issues with
// their issue counts, straight from the labels column (json_each) with no
// tracker call (ADR 0007). Labels are grouped case-insensitively, consistent
// with the board's label filter, and tombstoned issues are excluded.
func (s *Issues) Labels(repo string) (labels []LabelCount, err error) {
	rows, err := s.db.Query(
		`SELECT min(value), count(DISTINCT i.id)
		 FROM issues i, json_each(i.labels)
		 WHERE i.repo = ? AND i.deleted_at = ''
		 GROUP BY lower(value)
		 ORDER BY lower(value)`,
		repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	labels = []LabelCount{}
	for rows.Next() {
		var lc LabelCount
		if scanErr := rows.Scan(&lc.Name, &lc.Count); scanErr != nil {
			return nil, scanErr
		}
		labels = append(labels, lc)
	}
	return labels, rows.Err()
}

// AssigneeCount is one distinct assignee carried by a repo's issues and the
// number of issues assigned to them.
type AssigneeCount struct {
	ID    string
	Name  string
	Count int
}

// Assignees returns the distinct assignees of a repo's stored issues with their
// issue counts, and the count of unassigned issues, straight from the issues
// table with no tracker call (ADR 0007). Assigned rows are ordered by count
// descending then name; tombstoned issues are excluded. The caller flags and pins
// the repo's Me — only the assignee ids already reflected on the issues are
// returned, never the stored Me identity (ADR 0014).
func (s *Issues) Assignees(repo string) (assigned []AssigneeCount, unassigned int, err error) {
	rows, err := s.db.Query(
		`SELECT assignee_id, min(assignee_name), count(*)
		 FROM issues
		 WHERE repo = ? AND deleted_at = '' AND assignee_id IS NOT NULL
		 GROUP BY assignee_id
		 ORDER BY count(*) DESC, min(assignee_name)`,
		repo,
	)
	if err != nil {
		return nil, 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	assigned = []AssigneeCount{}
	for rows.Next() {
		var (
			ac   AssigneeCount
			name sql.NullString
		)
		if scanErr := rows.Scan(&ac.ID, &name, &ac.Count); scanErr != nil {
			return nil, 0, scanErr
		}
		ac.Name = name.String
		assigned = append(assigned, ac)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	err = s.db.QueryRow(
		`SELECT count(*) FROM issues WHERE repo = ? AND deleted_at = '' AND assignee_id IS NULL`,
		repo,
	).Scan(&unassigned)
	return assigned, unassigned, err
}

// cleanGroups trims the requested state groups and drops blanks, so a stray empty
// value narrows nothing rather than matching a nonexistent group.
func cleanGroups(groups []string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
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
			id                     int64
			labels                 string
			hasCh                  int
			assigneeID, assigneeNm sql.NullString
			iss                    = Issue{Repo: repo}
		)
		if err := rows.Scan(
			&id, &iss.Source, &iss.Identifier, &iss.Title, &iss.Description,
			&iss.Status, &iss.StatusGroup, &iss.Priority, &labels, &iss.Parent,
			&hasCh, &iss.DueDate, &iss.ExternalID, &iss.URL, &iss.CreatedAt, &iss.UpdatedAt,
			&iss.DeletedAt, &iss.ArchivedAt, &assigneeID, &assigneeNm,
		); err != nil {
			return nil, nil, err
		}
		iss.HasChildren = hasCh != 0
		iss.Labels = decodeLabels(labels)
		iss.AssigneeID = assigneeID.String
		iss.AssigneeName = assigneeNm.String
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
		 WHERE issues_fts MATCH ? AND i.repo = ? AND i.deleted_at = ''
		 ORDER BY bm25(issues_fts, 10.0, 5.0, 1.0, 3.0, 3.0)
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
		`SELECT team_id, project_id, project, cursor, last_synced_at, last_issues, last_comments, last_error,
			me_id, me_name, me_resolved_at
		 FROM issue_sync WHERE repo = ?`,
		repo,
	).Scan(
		&st.Binding.TeamID, &st.Binding.ProjectID, &st.Binding.Project, &st.Cursor,
		&st.LastSyncedAt, &st.LastIssues, &st.LastComments, &st.LastError,
		&st.Me.ID, &st.Me.Name, &st.Me.ResolvedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{}, nil
	}
	if err != nil {
		return SyncState{}, err
	}
	return st, nil
}

// Count returns how many of a repo's issues the store holds, across every source
// and excluding the tombstoned rows the board hides, so it matches the issue
// total the backlog shows rather than the raw row count.
func (s *Issues) Count(repo string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM issues WHERE repo = ? AND deleted_at = ''`,
		repo,
	).Scan(&n)
	return n, err
}

// SetArchived stamps or clears a repo issue's archive tombstone regardless of its
// source, returning the updated issue and whether it was found. Archiving records
// the current time; unarchiving clears it. Sync never writes archived_at — Upsert
// leaves the default and Reconcile's revival path clears only deleted_at — so the
// archive state an issue carries survives every later pull.
func (s *Issues) SetArchived(repo, identifier string, archived bool) (Issue, bool, error) {
	stamp := ""
	if archived {
		stamp = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := s.db.Exec(
		`UPDATE issues SET archived_at = ? WHERE repo = ? AND identifier = ?`,
		stamp, repo, identifier,
	)
	if err != nil {
		return Issue{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Issue{}, false, err
	} else if n == 0 {
		return Issue{}, false, nil
	}
	return s.Get(repo, identifier)
}

// ArchivedCount returns how many of a repo's issues are explicitly archived — one
// row per archived epic or leaf, since archiving an epic stamps only the epic and
// its children hide by family. It backs the board's "Archived (N)" toggle badge.
func (s *Issues) ArchivedCount(repo string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM issues WHERE repo = ? AND archived_at <> ''`,
		repo,
	).Scan(&n)
	return n, err
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

// SaveIdentity persists the repo binding's resolved Me — the tracker user behind
// its credentials — stamping when it was resolved, so later reads show who the hub
// treats as Me for the repo. It touches only the identity columns, leaving the
// binding, cursor, and last-sync outcome intact.
func (s *Issues) SaveIdentity(repo, id, name string) error {
	_, err := s.db.Exec(
		`INSERT INTO issue_sync(repo, me_id, me_name, me_resolved_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(repo) DO UPDATE SET
			me_id = excluded.me_id, me_name = excluded.me_name,
			me_resolved_at = excluded.me_resolved_at`,
		repo, id, name, time.Now().UTC().Format(time.RFC3339Nano),
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

// ClearError drops any recorded sync error from the repo's bookkeeping row,
// leaving the binding, cursor, counts, and last-synced time intact. It is the
// escape hatch for a repo whose provider no longer pulls — explicitly internal —
// where no successful RecordResult will ever run to clear a stale error.
func (s *Issues) ClearError(repo string) error {
	_, err := s.db.Exec(`UPDATE issue_sync SET last_error = '' WHERE repo = ?`, repo)
	return err
}

// Get returns a repo's stored issue by identifier regardless of source, without
// its comments, and whether it was found. It answers by-id lookups — like run
// detail — that need an issue's stored state, including whether it was tombstoned
// (DeletedAt set) after being removed from the tracker.
func (s *Issues) Get(repo, identifier string) (iss Issue, found bool, err error) {
	rows, err := s.db.Query(
		`SELECT `+issueColumns+` FROM issues WHERE repo = ? AND identifier = ?`,
		repo, identifier,
	)
	if err != nil {
		return Issue{}, false, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	issues, _, err := scanIssues(repo, rows)
	if err != nil {
		return Issue{}, false, err
	}
	if len(issues) == 0 {
		return Issue{}, false, nil
	}
	return issues[0], true, nil
}

// Find returns a repo's stored issue by identifier regardless of source, with its
// comments attached, and whether it was found. It is the by-id read the pipeline's
// store-backed tracker uses to build prompts (description and comments) and to read
// status without a tracker call (ADR 0007). A tombstoned issue is returned with
// DeletedAt set so the caller can treat it as absent.
func (s *Issues) Find(repo, identifier string) (Issue, bool, error) {
	iss, found, err := s.Get(repo, identifier)
	if err != nil || !found {
		return Issue{}, found, err
	}
	comments, err := s.commentsFor(repo, identifier)
	if err != nil {
		return Issue{}, false, err
	}
	iss.Comments = comments
	return iss, true, nil
}

func (s *Issues) commentsFor(repo, identifier string) (comments []Comment, err error) {
	rows, err := s.db.Query(
		`SELECT c.external_id, c.author, c.body, c.created_at, c.updated_at
		 FROM issue_comments c JOIN issues i ON i.id = c.issue_id
		 WHERE i.repo = ? AND i.identifier = ? ORDER BY c.id`,
		repo, identifier,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	comments = []Comment{}
	for rows.Next() {
		var c Comment
		if scanErr := rows.Scan(&c.ExternalID, &c.Author, &c.Body, &c.CreatedAt, &c.UpdatedAt); scanErr != nil {
			return nil, scanErr
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// SyncedPatch mirrors a tracker write onto a stored synced issue: an optional new
// display status and status group, an optional replacement description, and label
// deltas. It carries the fields trau writes to a synced ticket — status and labels
// operationally, and the description once a grill apply replaces it — so a
// hub-initiated write lands in the store immediately and the next inbound sync sees
// no divergence to reconcile (ADR 0007).
type SyncedPatch struct {
	Status       string
	StatusGroup  string
	Description  string
	AddLabels    []string
	RemoveLabels []string
}

// UpdateSynced applies a tracker write's status/description/label change to a
// repo's stored synced issue so the board reflects the transition without waiting
// for the next sync (ADR 0007). It only ever touches a source<>'internal' row — a
// missing or internal identifier yields found=false — and returns the updated row.
// An empty Status, StatusGroup, or Description leaves that field unchanged; label
// deltas apply case-insensitively.
func (s *Issues) UpdateSynced(repo, identifier string, patch SyncedPatch) (Issue, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Issue{}, false, err
	}
	var (
		id          int64
		status      string
		group       string
		description string
		labelsRaw   string
	)
	err = tx.QueryRow(
		`SELECT id, status, status_group, description, labels FROM issues
		 WHERE repo = ? AND identifier = ? AND source <> ?`,
		repo, identifier, SourceInternal,
	).Scan(&id, &status, &group, &description, &labelsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, false, tx.Rollback()
	}
	if err != nil {
		return Issue{}, false, errors.Join(err, tx.Rollback())
	}
	if v := strings.TrimSpace(patch.Status); v != "" {
		status = v
	}
	if v := strings.TrimSpace(patch.StatusGroup); v != "" {
		group = v
	}
	if patch.Description != "" {
		description = patch.Description
	}
	labels := mergeLabels(decodeLabels(labelsRaw), patch.AddLabels, patch.RemoveLabels)
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return Issue{}, false, errors.Join(err, tx.Rollback())
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(
		`UPDATE issues SET status = ?, status_group = ?, description = ?, labels = ?, updated_at = ? WHERE id = ?`,
		status, group, description, string(labelsJSON), now, id,
	); err != nil {
		return Issue{}, false, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Issue{}, false, err
	}
	return s.Find(repo, identifier)
}

// Reconcile tombstones the repo's synced issues the tracker no longer returns and
// revives any that reappeared, given live — the Project's current full identifier
// set. A tombstoned issue keeps its row (a run may still reference it) but its
// deleted_at is stamped, excluding it from the board and search; a revived issue
// has deleted_at cleared. Internal issues are never touched (ADR 0007). It returns
// the identifiers it newly tombstoned so the caller can drop them from the Queue.
// Callers must not pass an empty live set for a Project that still has issues: it
// would tombstone every synced row.
func (s *Issues) Reconcile(repo string, live []string) (tombstoned []string, err error) {
	liveSet := make(map[string]struct{}, len(live))
	for _, id := range live {
		liveSet[id] = struct{}{}
	}
	rows, err := s.db.Query(
		`SELECT identifier, deleted_at FROM issues WHERE repo = ? AND source <> 'internal'`,
		repo,
	)
	if err != nil {
		return nil, err
	}
	var toRevive []string
	for rows.Next() {
		var identifier, deletedAt string
		if err := rows.Scan(&identifier, &deletedAt); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		_, alive := liveSet[identifier]
		switch {
		case !alive && deletedAt == "":
			tombstoned = append(tombstoned, identifier)
		case alive && deletedAt != "":
			toRevive = append(toRevive, identifier)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(tombstoned) == 0 && len(toRevive) == 0 {
		return nil, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range tombstoned {
		if _, err := tx.Exec(
			`UPDATE issues SET deleted_at = ? WHERE repo = ? AND identifier = ? AND source <> 'internal'`,
			stamp, repo, id,
		); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
	}
	for _, id := range toRevive {
		if _, err := tx.Exec(
			`UPDATE issues SET deleted_at = '' WHERE repo = ? AND identifier = ? AND source <> 'internal'`,
			repo, id,
		); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tombstoned, nil
}

// DropSynced deletes a repo's synced issues and resets its sync cursor so the next
// pull re-populates the Project from scratch — the force-resync recovery path when
// sync state is doubted (ADR 0007). Internal issues are preserved; comments cascade
// with their issue through the foreign key. The cached team/project binding is
// kept, so the re-pull reuses it without re-resolving.
func (s *Issues) DropSynced(repo string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM issues WHERE repo = ? AND source <> 'internal'`, repo); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if _, err := tx.Exec(
		`UPDATE issue_sync SET cursor = '', last_synced_at = '', last_issues = 0, last_comments = 0, last_error = ''
		 WHERE repo = ?`,
		repo,
	); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// DeleteSyncState drops a repo's tracker binding, sync cursor, and resolved Me
// identity — the unregister sweep. Left behind, they are the orphan rows a
// re-registered root silently resumes from: a cursor that skips everything the
// tracker changed while the repo was gone, against a binding it may no longer
// have. The repo's issues stay put, so its run history remains browsable, and the
// re-register re-resolves and re-pulls from scratch.
func (s *Issues) DeleteSyncState(repo string) error {
	_, err := s.db.Exec(`DELETE FROM issue_sync WHERE repo = ?`, repo)
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
