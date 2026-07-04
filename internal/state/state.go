// Package state is the durable per-ticket checkpoint layer that makes the loop
// resumable. Each ticket's progress lives in runs/<ID>/state as key=value lines
// (PHASE, BRANCH, PR, PR_URL, UPDATED), written under runs/ so it survives a
// reboot — never /tmp. It also owns the ordered phase ranking the resume logic
// keys off and the --status reporter.
//
// The file format and the phase ranking must stay stable across runs for
// checkpoints to remain readable.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/sanitize"
)

// Checkpoint phase values written to the PHASE key. The loop advances a ticket
// through these in order; Merged and Quarantined are terminal (resume skips them).
const (
	Building    = "building"
	Built       = "built"
	HandedOff   = "handed_off"
	Verified    = "verified"
	PROpen      = "pr_open"
	Merged      = "merged"
	Quarantined = "quarantined"
)

// Idx is the ordered rank of a checkpoint phase:
// building(1) → built(2) → handed_off(3) → verified(4) → pr_open(5) → merged(6),
// quarantined(9), and 0 for an unknown/empty phase. Anything ≥ 6 is terminal.
func Idx(phase string) int {
	switch phase {
	case Building:
		return 1
	case Built:
		return 2
	case HandedOff:
		return 3
	case Verified:
		return 4
	case PROpen:
		return 5
	case Merged:
		return 6
	case Quarantined:
		return 9
	default:
		return 0
	}
}

// Terminal reports whether a phase is at or beyond merged(6) — a finished or
// quarantined ticket that the resume scan must skip (rank >= 6).
func Terminal(phase string) bool { return Idx(phase) >= 6 }

// Reconcilable reports whether a checkpoint phase is worth cross-checking against
// the tracker: any tracked attempt that is not already merged locally — an
// in-flight phase (rank 1–5) or a quarantined one (rank 9). Merged (6) and
// unknown/empty (0) phases are skipped, since neither can be a stale "problem"
// left over after the work shipped out-of-band.
func Reconcilable(phase string) bool {
	r := Idx(phase)
	return r != 0 && r != 6
}

// StaleCheckpoint reports whether a local checkpoint should be cleared during
// reconciliation: a Reconcilable phase whose tracker issue is already terminal
// (Done/Canceled, trackerDone=true). A still-open tracker issue is always left
// intact, as is a locally-merged or unknown checkpoint.
func StaleCheckpoint(phase string, trackerDone bool) bool {
	return trackerDone && Reconcilable(phase)
}

// Store reads and writes per-ticket checkpoints under a runs/ root (the same
// root the token sink uses).
type Store struct {
	root string
	now  func() time.Time
}

// NewStore returns a Store rooting state files at <root>/<ID>/state.
func NewStore(root string) *Store { return &Store{root: root, now: time.Now} }

// WithClock overrides the UPDATED timestamp source; intended for tests.
func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

// Root returns the runs/ directory this Store reads/writes under.
func (s *Store) Root() string { return s.root }

func (s *Store) file(id string) string { return filepath.Join(s.root, id, "state") }

// ensureRunsGitignore drops a "*" .gitignore in the runs root so trau's own state
// and logs are never swept into the target repo by a `git add -A`. Best-effort and
// idempotent — a missing or unwritable runs dir simply leaves things as they were.
func ensureRunsGitignore(root string) {
	gi := filepath.Join(root, ".gitignore")
	if _, err := os.Stat(gi); err == nil {
		return
	}
	_ = os.WriteFile(gi, []byte("# trau run artifacts — do not commit\n*\n"), 0o644)
}

// Get returns the value of key in ticket id's state file, or "" when the file or
// key is absent. The value is everything after the first '=' (so values may
// contain '='); on duplicate keys the last wins.
func (s *Store) Get(id, key string) string {
	data, err := os.ReadFile(s.file(id))
	if err != nil {
		return ""
	}
	prefix := key + "="
	val := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			val = line[len(prefix):]
		}
	}
	return val
}

// Set upserts key=value and refreshes the UPDATED timestamp, last-write-wins per
// key: existing key= and UPDATED= lines are dropped, every other line keeps its
// order, then key=value and UPDATED= are appended. The write is atomic (temp
// file + rename within runs/<ID>/).
func (s *Store) Set(id, key, value string) error {
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ensureRunsGitignore(s.root)
	var kept []string
	if data, err := os.ReadFile(s.file(id)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, key+"=") || strings.HasPrefix(line, "UPDATED=") {
				continue
			}
			kept = append(kept, line)
		}
	}
	kept = append(kept, key+"="+sanitize.StateValue(value))
	kept = append(kept, "UPDATED="+s.now().Format("2006-01-02 15:04:05"))
	out := strings.Join(kept, "\n") + "\n"

	tmp, err := os.CreateTemp(dir, "state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.file(id))
}

// Tickets returns the ids of every ticket with a saved state file, in the
// lexicographic order of the runs/*/state glob. The _loop token bucket has no
// state file and is naturally excluded.
func (s *Store) Tickets() []string {
	matches, _ := filepath.Glob(filepath.Join(s.root, "*", "state"))
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, filepath.Base(filepath.Dir(m)))
	}
	return ids
}

// ResumeTarget returns the lowest-numbered ticket with an in-flight checkpoint
// (rank 1–5) and its phase, or ("", "") when none. It scans local state only (no
// MCP call), skips terminal (rank ≥ 6) and unknown (rank 0) phases, and orders by
// the numeric part of the id, not lexicographically (so COD-9 sorts before
// COD-10). This is the authoritative "where did we leave off" signal for the main
// loop.
func (s *Store) ResumeTarget() (id, phase string) {
	return s.ResumeTargetFunc(nil)
}

// ResumeTargetFunc is ResumeTarget restricted to the ids the keep predicate
// accepts. A nil predicate keeps every id (identical to ResumeTarget). The epic
// flow passes a child-set membership test so a stale checkpoint for a ticket that
// is not part of the requested epic — even one in the same runs/ dir — is skipped
// rather than resumed.
func (s *Store) ResumeTargetFunc(keep func(id string) bool) (id, phase string) {
	bestNum := math.MaxInt
	for _, t := range s.Tickets() {
		if keep != nil && !keep(t) {
			continue
		}
		ph := s.Get(t, "PHASE")
		if rank := Idx(ph); rank == 0 || rank >= 6 {
			continue
		}
		num, ok := ticketNum(t)
		if !ok {
			continue
		}
		if num < bestNum {
			bestNum, id, phase = num, t, ph
		}
	}
	return id, phase
}

var reTicketNum = regexp.MustCompile(`[0-9]+`)

func ticketNum(id string) (int, bool) {
	ms := reTicketNum.FindAllString(id, -1)
	if len(ms) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(ms[len(ms)-1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// atoiSafe parses a stored integer field, yielding 0 for an empty or malformed
// value so an absent checkpoint field reads as zero rather than erroring.
func atoiSafe(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}

// RemoveState deletes ticket id's state file (runs/<ID>/state), leaving the rest
// of the run directory (logs, tokens.jsonl) intact, so a stuck attempt can be
// reset and re-queued. A missing file is not an error (idempotent reset).
func (s *Store) RemoveState(id string) error {
	if err := os.Remove(s.file(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Bucket is a non-ticket token bucket surfaced in --status beside the saved ticket
// rows — the planning bucket, whose spend carries no ticket checkpoint of its own.
// ID is the runs/ bucket dir passed to the total func; Label is the row/JSON name.
// Its tokens fold into the grand total. A bucket with no logged spend is omitted.
type Bucket struct {
	ID    string
	Label string
}

type bucketRow struct {
	label   string
	tokens  int
	cost    float64
	metered bool
}

// bucketRows resolves each extra bucket's spend via total, dropping the ones with
// no logged tokens so an unused bucket never clutters the report.
func bucketRows(extra []Bucket, total func(id string) (int, float64, bool)) []bucketRow {
	rows := make([]bucketRow, 0, len(extra))
	for _, b := range extra {
		tok, cost, metered := total(b.ID)
		if tok == 0 {
			continue
		}
		rows = append(rows, bucketRow{label: b.Label, tokens: tok, cost: cost, metered: metered})
	}
	return rows
}

// Status writes the --status report to w: a header, one row per ticket with
// saved state (ID, PHASE, TOKENS, COST, PR), a row per non-ticket bucket that has
// spend (extra, e.g. the planning bucket), and a grand-total row. total supplies
// each id's (tokens, cost) — the caller injects tokens.Sink.Total, keeping this
// package independent of the tokens package. It never errors; an empty runs/ with
// no bucket spend prints a "no saved state" line.
func (s *Store) Status(w io.Writer, total func(id string) (tokens int, cost float64, metered bool), extra ...Bucket) {
	_, _ = fmt.Fprintf(w, "  %-10s %-12s %12s %9s %5s  %s\n", "ID", "PHASE", "TOKENS", "COST", "ANOM", "PR")

	ids := s.Tickets()
	buckets := bucketRows(extra, total)
	if len(ids) == 0 && len(buckets) == 0 {
		_, _ = fmt.Fprintf(w, "  (no saved ticket state in %s)\n", s.root)
		return
	}

	var grandTokens int
	var grandCost float64
	grandMetered := true
	for _, id := range ids {
		phase := s.Get(id, "PHASE")
		if phase == "" {
			phase = "?"
		}
		tok, cost, metered := total(id)
		_, _ = fmt.Fprintf(w, "  %-10s %-12s %12d %8s %5s  %s\n", id, phase, tok, fmtCostCell(cost, metered), s.Get(id, "ANOMALIES"), s.Get(id, "PR_URL"))
		grandTokens += tok
		grandCost = math.Round((grandCost+cost)*100) / 100
		grandMetered = grandMetered && metered
	}
	for _, b := range buckets {
		_, _ = fmt.Fprintf(w, "  %-10s %-12s %12d %8s %5s  %s\n", b.label, "", b.tokens, fmtCostCell(b.cost, b.metered), "", "")
		grandTokens += b.tokens
		grandCost = math.Round((grandCost+b.cost)*100) / 100
		grandMetered = grandMetered && b.metered
	}
	_, _ = fmt.Fprintf(w, "  %-10s %-12s %12d %8s\n", "TOTAL", "", grandTokens, fmtCostCell(grandCost, grandMetered))
}

// StatusJSON writes the saved checkpoints as a single machine-readable JSON
// object: a tickets array (id/title/phase/pr_url/tokens/cost) plus a summed
// total. It mirrors Status's data but stays byte-stable for scripts piping
// `trau --status --json` into jq. No header line is written, so stdout carries
// only the JSON document. budget, when non-nil, is marshaled under a "budget" key
// (the configured caps + the day's spend); state takes it as any so it need not
// depend on the budget package.
func (s *Store) StatusJSON(w io.Writer, total func(id string) (tokens int, cost float64, metered bool), budget any, reconciled []string, extra ...Bucket) error {
	type ticket struct {
		ID            string  `json:"id"`
		Title         string  `json:"title,omitempty"`
		Phase         string  `json:"phase"`
		PRURL         string  `json:"pr_url,omitempty"`
		FailureReason string  `json:"failure_reason,omitempty"`
		Tokens        int     `json:"tokens"`
		Cost          float64 `json:"cost"`
		CostMeasured  bool    `json:"cost_measured"`
		Anomalies     int     `json:"anomalies,omitempty"`
	}
	type bucket struct {
		Label        string  `json:"label"`
		Tokens       int     `json:"tokens"`
		Cost         float64 `json:"cost"`
		CostMeasured bool    `json:"cost_measured"`
	}
	var report struct {
		Tickets []ticket `json:"tickets"`
		Buckets []bucket `json:"buckets,omitempty"`
		Total   struct {
			Tokens       int     `json:"tokens"`
			Cost         float64 `json:"cost"`
			CostMeasured bool    `json:"cost_measured"`
		} `json:"total"`
		Budget     any      `json:"budget,omitempty"`
		Reconciled []string `json:"reconciled,omitempty"`
	}

	report.Tickets = []ticket{}
	report.Total.CostMeasured = true
	report.Budget = budget
	report.Reconciled = reconciled
	for _, id := range s.Tickets() {
		tok, cost, metered := total(id)
		report.Tickets = append(report.Tickets, ticket{
			ID:            id,
			Title:         s.Get(id, "TITLE"),
			Phase:         s.Get(id, "PHASE"),
			PRURL:         s.Get(id, "PR_URL"),
			FailureReason: s.Get(id, "FAILURE_REASON"),
			Tokens:        tok,
			Cost:          cost,
			CostMeasured:  metered,
			Anomalies:     atoiSafe(s.Get(id, "ANOMALIES")),
		})
		report.Total.Tokens += tok
		report.Total.Cost = math.Round((report.Total.Cost+cost)*100) / 100
		report.Total.CostMeasured = report.Total.CostMeasured && metered
	}
	for _, b := range bucketRows(extra, total) {
		report.Buckets = append(report.Buckets, bucket{Label: b.label, Tokens: b.tokens, Cost: b.cost, CostMeasured: b.metered})
		report.Total.Tokens += b.tokens
		report.Total.Cost = math.Round((report.Total.Cost+b.cost)*100) / 100
		report.Total.CostMeasured = report.Total.CostMeasured && b.metered
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func fmtCost(cost float64) string {
	return strconv.FormatFloat(cost, 'f', -1, 64)
}

// fmtCostCell renders a COST cell honestly: "$1.23" when fully metered, "n/a"
// when no per-call dollar cost was measured (kimi/codex subscription phases that
// log tokens but no dollars), and "$1.23+" when the figure is a lower bound
// because some calls were unmetered.
func fmtCostCell(cost float64, metered bool) string {
	switch {
	case metered:
		return "$" + fmtCost(cost)
	case cost == 0:
		return "n/a"
	default:
		return "$" + fmtCost(cost) + "+"
	}
}
