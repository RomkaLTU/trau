package hubstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/registry"
)

var (
	// ErrProjectNotFound is returned when a rename, delete, or membership change
	// targets an identifier no project holds.
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectRepoNotFound is returned when a removal targets a root the project
	// does not hold.
	ErrProjectRepoNotFound = errors.New("repo is not a member of the project")
	// ErrProjectNameEmpty is returned when a create or rename carries no display
	// name to show the group under.
	ErrProjectNameEmpty = errors.New("project name is empty")
)

// Project is a logical grouping of repo roots: a slug identifier, the display
// name it is shown under, and its member roots in the order they were added. A
// root belongs to at most one project.
type Project struct {
	ID    string
	Name  string
	Repos []string
}

// Projects is the hub's store of those groupings. It only ever references repo
// roots, so nothing it does can change the set of repos the hub knows or is
// allowed to start loops in.
type Projects struct {
	db *sql.DB
}

// NewProjects returns a store over db. The caller owns db's lifecycle.
func NewProjects(db *sql.DB) *Projects {
	return &Projects{db: db}
}

// List returns every project with its members, ordered by display name.
func (p *Projects) List() (projects []Project, err error) {
	members, err := p.members()
	if err != nil {
		return nil, err
	}
	rows, err := p.db.Query(`SELECT id, name FROM projects ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	projects = []Project{}
	for rows.Next() {
		var proj Project
		if err := rows.Scan(&proj.ID, &proj.Name); err != nil {
			return nil, err
		}
		proj.Repos = members[proj.ID]
		if proj.Repos == nil {
			proj.Repos = []string{}
		}
		projects = append(projects, proj)
	}
	return projects, rows.Err()
}

// Get returns one project with its members.
func (p *Projects) Get(id string) (Project, error) {
	proj := Project{ID: id}
	err := p.db.QueryRow(`SELECT name FROM projects WHERE id = ?`, id).Scan(&proj.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	proj.Repos, err = p.roots(id)
	return proj, err
}

// Holder returns the identifier of the project root belongs to, or "" when no
// project holds it.
func (p *Projects) Holder(root string) (string, error) {
	return holderOf(p.db, registry.CanonicalRoot(root))
}

// Create files a project under name, allocating a slug identifier unique across
// the store. It starts empty: repos join through AddRepo.
func (p *Projects) Create(name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, ErrProjectNameEmpty
	}
	tx, err := p.db.Begin()
	if err != nil {
		return Project{}, err
	}
	id, err := freeProjectID(tx, name)
	if err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := insertProject(tx, id, name); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, Repos: []string{}}, nil
}

// Rename replaces a project's display name, leaving its identifier and members
// untouched.
func (p *Projects) Rename(id, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, ErrProjectNameEmpty
	}
	res, err := p.db.Exec(`UPDATE projects SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return Project{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Project{}, err
	}
	if n == 0 {
		return Project{}, ErrProjectNotFound
	}
	return p.Get(id)
}

// Delete drops a project and its memberships. Its member roots stay registered
// and fall back to standalone repos.
func (p *Projects) Delete(id string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if n == 0 {
		return errors.Join(ErrProjectNotFound, tx.Rollback())
	}
	roots, err := memberRoots(tx, id)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, root := range roots {
		if err := clearSeeded(tx, root); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if _, err := tx.Exec(`DELETE FROM project_repos WHERE project = ?`, id); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := dropTracker(tx, id); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := dropTicketRepos(tx, id); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// AddRepo moves root into the project, appending it after the current members.
// A root belongs to one project at a time, so it leaves whichever project held
// it before, keeping the tracker keys that project seeded as its own.
func (p *Projects) AddRepo(id, root string) (Project, error) {
	root = registry.CanonicalRoot(root)
	tx, err := p.db.Begin()
	if err != nil {
		return Project{}, err
	}
	if err := requireProject(tx, id); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	prev, err := holderOf(tx, root)
	if err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if prev == id {
		if err := tx.Rollback(); err != nil {
			return Project{}, err
		}
		return p.Get(id)
	}
	if _, err := tx.Exec(`DELETE FROM project_repos WHERE root = ?`, root); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := clearSeeded(tx, root); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := appendMember(tx, id, root); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := pruneEmpty(tx, prev); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return p.Get(id)
}

// RemoveRepo takes root out of the project, leaving it registered and standalone.
// The project itself stays even when emptied — the caller is editing it.
func (p *Projects) RemoveRepo(id, root string) (Project, error) {
	root = registry.CanonicalRoot(root)
	tx, err := p.db.Begin()
	if err != nil {
		return Project{}, err
	}
	if err := requireProject(tx, id); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	res, err := tx.Exec(`DELETE FROM project_repos WHERE project = ? AND root = ?`, id, root)
	if err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if n == 0 {
		return Project{}, errors.Join(ErrProjectRepoNotFound, tx.Rollback())
	}
	if err := clearSeeded(tx, root); err != nil {
		return Project{}, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return p.Get(id)
}

// ForgetRoot drops root's membership, for a repo leaving the hub altogether.
func (p *Projects) ForgetRoot(root string) error {
	root = registry.CanonicalRoot(root)
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	held, err := holderOf(tx, root)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if held == "" {
		return tx.Rollback()
	}
	if _, err := tx.Exec(`DELETE FROM project_repos WHERE root = ?`, root); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := clearSeeded(tx, root); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := pruneEmpty(tx, held); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// EnsureRoots files every root no project holds into its own single-member
// project named after the repo directory. It is idempotent: a root already in a
// project stays where it is.
func (p *Projects) EnsureRoots(roots []string) error {
	if len(roots) == 0 {
		return nil
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	for _, root := range roots {
		root = registry.CanonicalRoot(root)
		if root == "" {
			continue
		}
		held, err := holderOf(tx, root)
		if err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if held != "" {
			continue
		}
		name := filepath.Base(root)
		id, err := freeProjectID(tx, name)
		if err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := insertProject(tx, id, name); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := appendMember(tx, id, root); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

// Canonicalize rewrites every membership root to its canonical spelling, keeping
// the first project that claimed the directory when two spellings of it landed in
// different ones. Memberships are matched byte-equal, so a root left under a
// second spelling hides the project a repo already belongs to and EnsureRoots
// mints it another. The keys each root holds on its project's behalf move with it.
func (p *Projects) Canonicalize() error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	members, err := allMembers(tx)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, group := range rootsToMerge(members, func(m projectMember) string { return m.root }) {
		canonical := registry.CanonicalRoot(group[0].root)
		for _, m := range group {
			if _, err := tx.Exec(`DELETE FROM project_repos WHERE root = ?`, m.root); err != nil {
				return errors.Join(err, tx.Rollback())
			}
		}
		if err := appendMember(tx, group[0].project, canonical); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	if err := canonicalizeSeeded(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

// projectMember is one row of project_repos: the project holding a root and the
// root as it is stored.
type projectMember struct {
	project string
	root    string
}

// allMembers returns every membership in the order the store took it. Rows are
// read by rowid rather than by project and position, because a merge picks the
// project that claimed a directory first and only insertion order says which that
// was — ordering by project would hand the directory to the first slug instead.
func allMembers(tx *sql.Tx) (members []projectMember, err error) {
	rows, err := tx.Query(`SELECT project, root FROM project_repos ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var m projectMember
		if err := rows.Scan(&m.project, &m.root); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// PruneEmpty drops every project holding no repos, so the project auto-minted for
// a root that has since been pruned does not linger in the list. Serve start runs
// it, which is also when a project created from the web and never filled goes.
func (p *Projects) PruneEmpty() error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	ids, err := emptyProjectIDs(tx)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	for _, id := range ids {
		if err := pruneEmpty(tx, id); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}
	return tx.Commit()
}

func (p *Projects) members() (byProject map[string][]string, err error) {
	rows, err := p.db.Query(`SELECT project, root FROM project_repos ORDER BY project, position`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	byProject = map[string][]string{}
	for rows.Next() {
		var project, root string
		if err := rows.Scan(&project, &root); err != nil {
			return nil, err
		}
		byProject[project] = append(byProject[project], root)
	}
	return byProject, rows.Err()
}

func (p *Projects) roots(id string) (roots []string, err error) {
	rows, err := p.db.Query(`SELECT root FROM project_repos WHERE project = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	roots = []string{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func insertProject(tx *sql.Tx, id, name string) error {
	_, err := tx.Exec(
		`INSERT INTO projects(id, name, created_at) VALUES(?, ?, ?)`,
		id, name, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func appendMember(tx *sql.Tx, id, root string) error {
	var next int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position), 0) + 1 FROM project_repos WHERE project = ?`, id,
	).Scan(&next); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO project_repos(root, project, position) VALUES(?, ?, ?)`, root, id, next)
	return err
}

func requireProject(tx *sql.Tx, id string) error {
	var name string
	err := tx.QueryRow(`SELECT name FROM projects WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProjectNotFound
	}
	return err
}

func memberRoots(tx *sql.Tx, id string) (roots []string, err error) {
	rows, err := tx.Query(`SELECT root FROM project_repos WHERE project = ? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

// emptyProjectIDs returns the identifiers of the projects holding no repos. They
// are read out before the first delete because the transaction holds a single
// connection.
func emptyProjectIDs(tx *sql.Tx) (ids []string, err error) {
	rows, err := tx.Query(
		`SELECT id FROM projects WHERE NOT EXISTS(SELECT 1 FROM project_repos WHERE project = projects.id)`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// querier is the shared read surface of *sql.DB and *sql.Tx, so one lookup backs
// both the standalone Holder and the membership moves inside a transaction.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func holderOf(q querier, root string) (string, error) {
	var project string
	err := q.QueryRow(`SELECT project FROM project_repos WHERE root = ?`, root).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return project, err
}

// pruneEmpty drops a project whose last member moved away, so grouping a repo
// never leaves an empty auto-migrated project behind.
func pruneEmpty(tx *sql.Tx, id string) error {
	if id == "" {
		return nil
	}
	res, err := tx.Exec(
		`DELETE FROM projects WHERE id = ? AND NOT EXISTS(SELECT 1 FROM project_repos WHERE project = ?)`,
		id, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	return dropTracker(tx, id)
}

// freeProjectID slugifies name and disambiguates it against the identifiers
// already taken.
func freeProjectID(tx *sql.Tx, name string) (string, error) {
	base := projectSlug(name)
	for n := 1; ; n++ {
		id := base
		if n > 1 {
			id = base + "-" + strconv.Itoa(n)
		}
		var taken string
		err := tx.QueryRow(`SELECT id FROM projects WHERE id = ?`, id).Scan(&taken)
		if errors.Is(err, sql.ErrNoRows) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
	}
}

var reProjectSlug = regexp.MustCompile(`[^a-z0-9]+`)

// projectSlug reduces a display name to an identifier. A name carrying no
// alphanumerics yields "project", which the caller disambiguates.
func projectSlug(name string) string {
	slug := strings.Trim(reProjectSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		return "project"
	}
	return slug
}
