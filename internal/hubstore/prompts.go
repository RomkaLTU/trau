package hubstore

import (
	"database/sql"
	"errors"
	"time"

	"github.com/RomkaLTU/trau/internal/prompts"
)

// PromptOverrides is the store of user-edited prompt template bodies. A row's
// repo is the repo root it applies to, with "" the global scope; consumption
// resolves repo > global > built-in default. The caller owns db's lifecycle.
type PromptOverrides struct {
	db *sql.DB
}

// NewPromptOverrides returns a PromptOverrides store over db.
func NewPromptOverrides(db *sql.DB) *PromptOverrides { return &PromptOverrides{db: db} }

// Scope returns the overrides stored for one scope as a name → body map. repo
// "" is the global scope.
func (p *PromptOverrides) Scope(repo string) (out map[string]string, err error) {
	rows, err := p.db.Query(`SELECT name, body FROM prompt_overrides WHERE repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = map[string]string{}
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			return nil, err
		}
		out[name] = body
	}
	return out, rows.Err()
}

// Set upserts the override for name in repo's scope.
func (p *PromptOverrides) Set(name, repo, body string) error {
	_, err := p.db.Exec(
		`INSERT INTO prompt_overrides(name, repo, body, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(name, repo) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		name, repo, body, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// Delete removes the override for name in repo's scope. Deleting an absent row
// is a no-op.
func (p *PromptOverrides) Delete(name, repo string) error {
	_, err := p.db.Exec(`DELETE FROM prompt_overrides WHERE name = ? AND repo = ?`, name, repo)
	return err
}

// Effective returns the effective name → body map for repo: the repo override
// when set, else the global override, else the built-in default, for every
// prompt in the registry.
func (p *PromptOverrides) Effective(repo string) (map[string]string, error) {
	global, err := p.Scope("")
	if err != nil {
		return nil, err
	}
	scoped, err := p.Scope(repo)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, pr := range prompts.Catalog() {
		body := pr.Default
		if b, ok := global[pr.Name]; ok {
			body = b
		}
		if b, ok := scoped[pr.Name]; ok {
			body = b
		}
		out[pr.Name] = body
	}
	return out, nil
}
