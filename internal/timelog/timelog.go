// Package timelog writes per-ticket human-effort time logs as JSON under
// <repo>/.trau/time/<TICKET>.json, a format other time-tracking tools can read
// (the JSON schema stays dev-flow-compatible; only the directory moved).
//
// The feature is opt-in (off by default) and best-effort: callers gather the
// inputs, call Record, and log-and-continue on any error. Nothing here ever blocks
// or fails the loop. The recorded minutes are an ESTIMATE of senior-developer human
// effort (anchored to a fixed table), not agent wall-clock. See COD-622.
package timelog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Storage modes.
const (
	StorageRepo = "repo" // <repoRoot>/.trau/time/<TICKET>.json (default)
	StorageUser = "user" // ~/.trau/time/<repo-slug>/<TICKET>.json
	StorageNone = "none" // persist nothing
)

// Output formats for export / status rendering.
const (
	FormatDefault     = "default"      // canonical JSON (the persisted artifact)
	FormatJiraWorklog = "jira-worklog" // Jira worklog lines for copy-paste
	FormatTogglCSV    = "toggl-csv"    // Toggl-style CSV
	FormatPlain       = "plain"        // plain human-readable lines
)

// Estimator modes.
const (
	EstimatorHeuristic = "heuristic" // deterministic table mapping (default)
	EstimatorAgent     = "agent"     // cheap agent call (caller-provided)
)

// DiffStats is the per-entry change summary, mirrored from dev-flow.
type DiffStats struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// Entry is one day's logged effort for a ticket.
type Entry struct {
	Date      string    `json:"date"`
	Minutes   int       `json:"minutes"`
	Summary   string    `json:"summary"`
	DiffStats DiffStats `json:"diffStats"`
	Commits   []string  `json:"commits"`
}

// Log is the on-disk per-ticket record. Field order and JSON tags match the
// dev-flow schema exactly so downstream collectors parse it unchanged.
type Log struct {
	TicketID     string  `json:"ticketId"`
	TicketTitle  string  `json:"ticketTitle"`
	Branch       string  `json:"branch"`
	Started      string  `json:"started"`
	Completed    string  `json:"completed"`
	Entries      []Entry `json:"entries"`
	TotalMinutes int     `json:"totalMinutes"`
}

// Path returns the destination file for a ticket's time log under the chosen
// storage mode, or "" when nothing should be persisted (mode "none", or a root
// that cannot be resolved):
//
//	repo -> <repoRoot>/.trau/time/<TICKET>.json
//	user -> ~/.trau/time/<repo-slug>/<TICKET>.json
func Path(storage, repoRoot, ticketID string) string {
	ticketID = strings.TrimSpace(ticketID)
	if ticketID == "" {
		return ""
	}
	switch storage {
	case StorageNone:
		return ""
	case StorageUser:
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		return filepath.Join(home, ".trau", "time", repoSlug(repoRoot), ticketID+".json")
	default: // repo (also the empty/unknown fallback)
		if strings.TrimSpace(repoRoot) == "" {
			return ""
		}
		return filepath.Join(repoRoot, ".trau", "time", ticketID+".json")
	}
}

// MigrateLegacy moves time logs from the pre-rename location (.dev-flow/time/)
// to the current one (.trau/time/) so upgraded installs keep appending to their
// existing per-ticket logs instead of forking new ones. Files already present at
// the new location win; their legacy counterparts are left in place. Emptied
// legacy directories are pruned. Best-effort: callers log-and-continue on error.
func MigrateLegacy(storage, repoRoot string) error {
	var legacy, dir string
	var prune []string
	switch storage {
	case StorageUser:
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil
		}
		slug := repoSlug(repoRoot)
		legacy = filepath.Join(home, ".dev-flow", "time", slug)
		dir = filepath.Join(home, ".trau", "time", slug)
		prune = []string{legacy, filepath.Join(home, ".dev-flow", "time"), filepath.Join(home, ".dev-flow")}
	case StorageRepo:
		if strings.TrimSpace(repoRoot) == "" {
			return nil
		}
		legacy = filepath.Join(repoRoot, ".dev-flow", "time")
		dir = filepath.Join(repoRoot, ".trau", "time")
		prune = []string{legacy, filepath.Join(repoRoot, ".dev-flow")}
	default:
		return nil
	}

	entries, err := os.ReadDir(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(dir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // never clobber a log already written at the new location
		}
		if err := os.Rename(filepath.Join(legacy, e.Name()), dst); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Remove refuses non-empty directories, so this only prunes what emptied out.
	for _, p := range prune {
		_ = os.Remove(p)
	}
	return firstErr
}

// repoSlug derives a stable per-repo directory name from the repo root.
func repoSlug(repoRoot string) string {
	base := filepath.Base(strings.TrimRight(strings.TrimSpace(repoRoot), string(filepath.Separator)))
	switch base {
	case "", ".", string(filepath.Separator):
		return "repo"
	}
	return base
}

// Record creates or updates the per-ticket time log at path, appending entry
// unless an entry covering the same work already exists. It is idempotent:
// re-running an already-merged ticket must not duplicate entries. totalMinutes is
// recomputed and completed is refreshed on every call; started is set once (the
// first write wins). A "" path is a no-op (storage disabled).
func Record(path string, meta Log, entry Entry, started, completed string) error {
	if path == "" {
		return nil
	}
	log, err := read(path)
	if err != nil {
		return err
	}

	log.TicketID = firstNonEmpty(meta.TicketID, log.TicketID)
	log.TicketTitle = firstNonEmpty(meta.TicketTitle, log.TicketTitle)
	log.Branch = firstNonEmpty(meta.Branch, log.Branch)
	if log.Started == "" {
		log.Started = started
	}
	log.Completed = completed

	if !hasEntry(log.Entries, entry) {
		log.Entries = append(log.Entries, entry)
	}
	log.TotalMinutes = totalMinutes(log.Entries)

	return write(path, log)
}

// hasEntry reports whether entries already cover e's work. Identity is the commit
// set (order-independent), so a re-run that re-enumerates the same commits is a
// no-op. When neither side carries commits (e.g. the branch was already gone on a
// reconcile), it falls back to same-date matching so a second write on the same day
// still does not duplicate.
func hasEntry(entries []Entry, e Entry) bool {
	for _, ex := range entries {
		if len(e.Commits) > 0 && sameSet(ex.Commits, e.Commits) {
			return true
		}
		if len(e.Commits) == 0 && len(ex.Commits) == 0 && ex.Date == e.Date {
			return true
		}
	}
	return false
}

// EnsureGitignore adds ".trau/time/" to the target repo's .gitignore when it is
// not already ignored, so per-developer effort numbers are never committed. It does
// NOT blanket-ignore .trau/ — the project's .trau/checks/ verify library is meant to
// be committed. Best-effort: the .gitignore is created when missing.
func EnsureGitignore(repoRoot string) error {
	if strings.TrimSpace(repoRoot) == "" {
		return nil
	}
	const want = ".trau/time/"
	gi := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if gitignoreCovers(string(data)) {
		return nil
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("# trau time-tracking effort numbers (per-developer; do not commit)\n")
	b.WriteString(want + "\n")
	return os.WriteFile(gi, []byte(b.String()), 0o644)
}

// gitignoreCovers reports whether content already ignores the time-log dir, treating
// either the time dir or the whole .trau dir (with or without a trailing slash or
// leading slash) as sufficient coverage.
func gitignoreCovers(content string) bool {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimSuffix(line, "/")
		switch line {
		case ".trau/time", ".trau":
			return true
		}
	}
	return false
}

// WriteExport renders the JSON log at jsonPath into the given format and writes it
// to a sibling file (e.g. <TICKET>.plain.txt), so an OUTPUT_FORMAT other than
// "default" produces a copy-paste-ready export next to the canonical JSON. The JSON
// file stays the source of truth read by downstream collectors. No-op for the
// default/empty format.
func WriteExport(jsonPath, format string) error {
	if format == "" || format == FormatDefault {
		return nil
	}
	l, err := read(jsonPath)
	if err != nil {
		return err
	}
	return os.WriteFile(exportPath(jsonPath, format), []byte(Render(l, format)), 0o644)
}

// Render returns the time log in the requested export format for copy-paste. The
// default format is the canonical JSON (the persisted artifact); the others render
// human / Jira-worklog / Toggl-CSV views.
func Render(l Log, format string) string {
	switch format {
	case FormatJiraWorklog:
		return renderJiraWorklog(l)
	case FormatTogglCSV:
		return renderTogglCSV(l)
	case FormatPlain:
		return renderPlain(l)
	default:
		data, err := json.MarshalIndent(l, "", "  ")
		if err != nil {
			return ""
		}
		return string(data) + "\n"
	}
}

func renderPlain(l Log) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  (%s total)\n", l.TicketID, l.TicketTitle, formatDuration(l.TotalMinutes))
	for _, e := range l.Entries {
		fmt.Fprintf(&b, "  %s  %s  %s\n", e.Date, formatDuration(e.Minutes), e.Summary)
	}
	return b.String()
}

func renderJiraWorklog(l Log) string {
	var b strings.Builder
	for _, e := range l.Entries {
		fmt.Fprintf(&b, "%s %s %s — %s\n", l.TicketID, e.Date, formatDuration(e.Minutes), e.Summary)
	}
	return b.String()
}

func renderTogglCSV(l Log) string {
	var b strings.Builder
	b.WriteString("ticket,date,minutes,summary\n")
	for _, e := range l.Entries {
		fmt.Fprintf(&b, "%s,%s,%d,%s\n", l.TicketID, e.Date, e.Minutes, csvField(e.Summary))
	}
	return b.String()
}

// HeuristicMinutes maps a change's shape to a senior-developer effort estimate in
// minutes, anchored to a fixed effort table. It is deterministic — the same
// diffstats and commit count always yield the same number — and never returns zero,
// so an enabled run always logs a real estimate. The estimate is HUMAN effort, not
// agent wall-clock. Anchored against diff size, file count, and commit count (a
// proxy for distinct concerns / iteration), not raw line count alone.
func HeuristicMinutes(d DiffStats, commits int) int {
	lines := d.Additions + d.Deletions
	minutes := ladderMinutes(d.Files, lines)
	// More commits signals more distinct concerns / rework; nudge up modestly.
	switch {
	case commits >= 8:
		minutes += 60
	case commits >= 4:
		minutes += 30
	}
	return minutes
}

func ladderMinutes(files, lines int) int {
	switch {
	case files <= 1 && lines <= 10:
		return 20 // config tweak / typo / one-line fix (15–30)
	case files <= 1 && lines < 50:
		return 45 // small bug fix, single file (30–60)
	case files <= 4 && lines < 150:
		return 90 // bug fix with tests, 2–4 files (1–2h)
	case files <= 8 && lines < 400:
		return 180 // small feature, single component/endpoint (2–4h)
	case files <= 25 && lines < 800:
		return 300 // mechanical refactor across many files (upper 2–4h)
	case lines < 2000:
		return 480 // feature spanning UI + API + DB (lower 4–8h)
	default:
		return 720 // architectural change with deep design work (lower 1–3 days)
	}
}

// --- internals -------------------------------------------------------------

func read(path string) (Log, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Log{}, nil
	}
	if err != nil {
		return Log{}, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return Log{}, nil
	}
	var l Log
	if err := json.Unmarshal(data, &l); err != nil {
		return Log{}, fmt.Errorf("parse existing time log %s: %w", path, err)
	}
	return l, nil
}

func write(path string, l Log) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func totalMinutes(entries []Entry) int {
	total := 0
	for _, e := range entries {
		total += e.Minutes
	}
	return total
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func exportPath(jsonPath, format string) string {
	dir, file := filepath.Split(jsonPath)
	base := strings.TrimSuffix(file, ".json")
	return filepath.Join(dir, base+"."+exportExt(format))
}

func exportExt(format string) string {
	switch format {
	case FormatJiraWorklog:
		return "worklog.txt"
	case FormatTogglCSV:
		return "csv"
	default:
		return "txt"
	}
}

func formatDuration(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	h := minutes / 60
	m := minutes % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
