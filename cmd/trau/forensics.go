package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
)

const (
	// forensicsFollowPoll is how often --follow re-reads the hub for new events.
	forensicsFollowPoll = 1 * time.Second
	// forensicsPollTimeout bounds one follow poll so an unreachable hub never wedges
	// the tick.
	forensicsPollTimeout = 5 * time.Second
)

const forensicsUsage = `trau forensics — read-only incident queries over the run history

Usage:
  trau forensics runs   [--repo <path>] [--json]
      list a repo's runs with phase and failure class/reason
  trau forensics events [--repo <path>] [--ticket <ID>] [--since <dur>] [--kind <k>] [--grep <pat>] [--follow] [--limit <n>] [--json]
      dump or follow a repo's events, filtered by ticket, time window, kind, and a pattern over payloads
  trau forensics spend  <ID> [--repo <path>] [--json]
      show a ticket's spend summary (per-phase breakdown and total)

Queries read the serve hub over HTTP and autostart it when none is running.
--since accepts a duration (30m, 2h) or an RFC3339 timestamp.
--follow tails live events, emitting newline-delimited JSON under --json.
`

// runForensics dispatches the read-only forensics query subcommands. Each is strictly
// read-only — it queries the hub's authoritative stores over HTTP and never touches
// loop state — so it is safe to run mid-incident alongside a live loop.
func runForensics(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New("forensics: expected a subcommand: runs, events, or spend")}
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "runs":
		return forensicsRuns(ctx, rest, stdout, stderr)
	case "events":
		return forensicsEvents(ctx, rest, stdout, stderr)
	case "spend":
		return forensicsSpend(ctx, rest, stdout, stderr)
	case "-h", "--help":
		_, _ = fmt.Fprint(stdout, forensicsUsage)
		return nil
	default:
		return usageError{fmt.Errorf("forensics: unknown subcommand %q (want runs, events, or spend)", verb)}
	}
}

// forensicsFlags is the flag set shared across the forensics subcommands, parsed by
// the manual loop each verb runs.
type forensicsFlags struct {
	repo    string
	ticket  string
	kind    string
	grep    string
	since   string
	limit   int
	follow  bool
	json    bool
	verbose bool
	debug   bool
}

// forensicsHub loads the layered config for repo, brings the serve hub up when none
// is running (hub autostart, subject to SERVE_AUTOSTART and the exposure policy),
// and returns a client plus the hub-registered repo name. Autostart is best-effort:
// a hub that cannot be brought up leaves the query to fail with a clear error.
func forensicsHub(ctx context.Context, f forensicsFlags, stderr io.Writer) (*hubclient.Client, string, error) {
	logger.Init(stderr, f.verbose, f.debug)
	cfg, err := loadServeConfig(f.repo)
	if err != nil {
		return nil, "", console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if cfg.RepoRoot == "" {
		cfg.RepoRoot, _ = config.ResolveRepoRoot(f.repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	}
	name := repoName(cfg.RepoRoot)
	if name == "" {
		name = f.repo
	}
	if name == "" {
		return nil, "", usageError{errors.New("forensics: no repo resolved — run inside a git repo or pass --repo <path>")}
	}
	ensureHubForStore(ctx, cfg, stderr)
	return hubclient.New(hubBaseURL(cfg), cfg.ServeToken), name, nil
}

func forensicsRuns(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f, err := parseForensicsFlags("runs", args, false)
	if err != nil {
		return err
	}
	hub, name, err := forensicsHub(ctx, f, stderr)
	if err != nil {
		return err
	}
	runs, err := hub.Runs(ctx, name)
	if err != nil {
		return forensicsHubError(err, name)
	}
	if f.json {
		return json.NewEncoder(stdout).Encode(runs)
	}
	writeRunsTable(stdout, runs)
	return nil
}

func forensicsSpend(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f, err := parseForensicsFlags("spend", args, true)
	if err != nil {
		return err
	}
	if f.ticket == "" {
		return usageError{errors.New("forensics spend: a ticket id is required (e.g. trau forensics spend COD-834)")}
	}
	hub, name, err := forensicsHub(ctx, f, stderr)
	if err != nil {
		return err
	}
	summary, err := hub.TicketSpend(ctx, name, f.ticket)
	if err != nil {
		return forensicsHubError(err, name)
	}
	if f.json {
		return json.NewEncoder(stdout).Encode(summary)
	}
	writeSpendSummary(stdout, summary)
	return nil
}

func forensicsEvents(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f, err := parseForensicsFlags("events", args, false)
	if err != nil {
		return err
	}
	since, err := resolveSince(f.since)
	if err != nil {
		return usageError{err}
	}
	hub, name, err := forensicsHub(ctx, f, stderr)
	if err != nil {
		return err
	}
	q := hubclient.EventQuery{Kind: f.kind, Ticket: f.ticket, Grep: f.grep, Since: since, Limit: f.limit}
	if f.follow {
		return followEvents(ctx, hub, name, q, f.json, stdout, stderr)
	}
	events, err := hub.QueryEvents(ctx, name, q)
	if err != nil {
		return forensicsHubError(err, name)
	}
	if f.json {
		return json.NewEncoder(stdout).Encode(events)
	}
	for _, e := range events {
		writeEventLine(stdout, e)
	}
	if len(events) == 0 {
		_, _ = fmt.Fprintln(stderr, "No matching events.")
	}
	return nil
}

// followEvents prints the recent matching window, then polls the hub for newer
// matching events until the context is cancelled — the headless events tail. Under
// jsonOut it emits newline-delimited JSON so the stream stays pipeable.
func followEvents(ctx context.Context, hub *hubclient.Client, repo string, q hubclient.EventQuery, jsonOut bool, stdout, stderr io.Writer) error {
	enc := json.NewEncoder(stdout)
	emit := func(e hubclient.EventRecord) {
		if jsonOut {
			_ = enc.Encode(e)
			return
		}
		writeEventLine(stdout, e)
	}

	events, err := hub.QueryEvents(ctx, repo, q)
	if err != nil {
		return forensicsHubError(err, repo)
	}
	var cursor int64
	for _, e := range events {
		emit(e)
		cursor = maxCursor(cursor, e.ID)
	}

	t := time.NewTicker(forensicsFollowPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		pollCtx, cancel := context.WithTimeout(ctx, forensicsPollTimeout)
		fq := q
		fq.After = cursor
		batch, err := hub.QueryEvents(pollCtx, repo, fq)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Verbosef("forensics events poll: %v", err)
			continue
		}
		for _, e := range batch {
			emit(e)
			cursor = maxCursor(cursor, e.ID)
		}
	}
}

// parseForensicsFlags reads the shared forensics flags with the manual arg loop the
// other subcommands use. When wantTicket is set, the first bare argument is taken as
// the ticket id.
func parseForensicsFlags(verb string, args []string, wantTicket bool) (forensicsFlags, error) {
	var f forensicsFlags
	i := 0
	next := func(flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", flag)
		}
		i++
		return args[i], nil
	}
	take := func(dst *string, flag string) error {
		v, err := next(flag)
		if err != nil {
			return err
		}
		*dst = v
		return nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		var err error
		switch a {
		case "--repo":
			err = take(&f.repo, a)
		case "--ticket":
			err = take(&f.ticket, a)
		case "--kind":
			err = take(&f.kind, a)
		case "--grep":
			err = take(&f.grep, a)
		case "--since":
			err = take(&f.since, a)
		case "--limit":
			v, verr := next(a)
			if verr != nil {
				err = verr
				break
			}
			n, verr := strconv.Atoi(v)
			if verr != nil || n <= 0 {
				return f, usageError{fmt.Errorf("forensics %s: invalid --limit %q", verb, v)}
			}
			f.limit = n
		case "--follow":
			f.follow = true
		case "--json":
			f.json = true
		case "--verbose":
			f.verbose = true
		case "--debug":
			f.debug = true
		default:
			if wantTicket && f.ticket == "" && !strings.HasPrefix(a, "-") {
				f.ticket = a
				continue
			}
			return f, usageError{fmt.Errorf("forensics %s: unknown arg: %s", verb, a)}
		}
		if err != nil {
			return f, usageError{err}
		}
	}
	return f, nil
}

// resolveSince turns a --since value into the RFC3339 lower bound the hub expects. A
// duration (30m, 2h) is read as a window ending now; an RFC3339 timestamp is used as
// given. An empty value means no lower bound.
func resolveSince(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().Add(-d).Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("forensics events: invalid --since %q (want a duration like 30m or 2h, or an RFC3339 timestamp)", raw)
}

func writeRunsTable(w io.Writer, runs []hubclient.RunSummary) {
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(w, "No runs recorded for this repo.")
		return
	}
	_, _ = fmt.Fprintf(w, "%-14s %-12s %-10s %s\n", "TICKET", "PHASE", "CLASS", "REASON")
	for _, r := range runs {
		_, _ = fmt.Fprintf(w, "%-14s %-12s %-10s %s\n", r.Ticket, r.Phase, r.FailureClass, r.FailureReason)
	}
}

func writeSpendSummary(w io.Writer, s hubclient.SpendSummary) {
	_, _ = fmt.Fprintf(w, "%s spend: %d tokens · %s\n", s.Ticket, s.Total.Tokens, fmtSpendCost(s.Total.Cost, s.Total.Metered))
	if len(s.Phases) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "  %-12s %12s %10s %6s %6s\n", "PHASE", "TOKENS", "COST", "TURNS", "CALLS")
	for _, p := range s.Phases {
		_, _ = fmt.Fprintf(w, "  %-12s %12d %10s %6d %6d\n", p.Phase, p.Tokens, fmtSpendCost(p.Cost, p.Metered), p.Turns, p.Calls)
	}
}

// fmtSpendCost renders a cost cell, marking an unmetered figure — one folded from a
// call a provider reported no per-call cost for — as a lower bound with a trailing +.
func fmtSpendCost(cost float64, metered bool) string {
	switch {
	case metered:
		return "$" + strconv.FormatFloat(cost, 'f', 2, 64)
	case cost == 0:
		return "n/a"
	default:
		return "$" + strconv.FormatFloat(cost, 'f', 2, 64) + "+"
	}
}

func writeEventLine(w io.Writer, e hubclient.EventRecord) {
	var b strings.Builder
	b.WriteString(e.TS)
	b.WriteByte(' ')
	b.WriteString(e.Kind)
	if e.Phase != "" {
		b.WriteString(" [")
		b.WriteString(e.Phase)
		b.WriteByte(']')
	}
	if e.Msg != "" {
		b.WriteString("  ")
		b.WriteString(e.Msg)
	}
	if len(e.Fields) > 0 {
		b.WriteString("  ")
		b.WriteString(formatEventFields(e.Fields))
	}
	_, _ = fmt.Fprintln(w, b.String())
}

// formatEventFields renders a fields bag as sorted key=value pairs so an event line
// stays greppable, matching the old event-log workflow.
func formatEventFields(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(fieldValue(m[k]))
	}
	return b.String()
}

func fieldValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func maxCursor(cur int64, id string) int64 {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= cur {
		return cur
	}
	return n
}

// forensicsHubError translates a hub read failure into an actionable CLI error: an
// unreachable hub the autostart could not bring up, or a repo/ticket the hub has
// never seen.
func forensicsHubError(err error, repo string) error {
	switch {
	case hubclient.IsUnreachable(err):
		return console.Actionable(err, "reach the web hub",
			"start it with `trau serve`, or set SERVE_AUTOSTART=1")
	case errors.Is(err, hubclient.ErrNotFound):
		return console.Actionable(err, "query "+repo,
			"the hub has not seen a run in this repo yet — run a loop here first, or pass --repo")
	default:
		return err
	}
}
