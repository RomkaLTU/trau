package webserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/proofsbranch"
)

// proofRetentionInterval bounds how often the hub prunes proofs past their
// retention window, on top of the startup pass.
const proofRetentionInterval = 24 * time.Hour

// EnableProofRetention arms the daily proof-retention sweep with the configured
// window (PROOF_RETENTION_DAYS) and resolves the browser-harness recordings root
// its trace-deletion guard is scoped to. A non-positive window keeps proofs
// forever and the sweep does nothing. Call it once, before Start.
func (s *Server) EnableProofRetention(days int) {
	s.proofRetention = days
	s.recordingsRoot = browserRecordingsRoot()
}

// pruneProofs drops proofs past the retention window on startup and on a daily
// timer: a run's raw local trace (deleted from disk, the column cleared) and its
// directory on the trau-proofs orphan branch (rebuilt as a fresh single commit).
// Hub-stored screenshots and rendered videos are kept forever. Each pass is
// best-effort hygiene, so a failure is logged and retried on the next tick.
func (s *Server) pruneProofs(ctx context.Context) {
	if s.proofRetention <= 0 {
		return
	}
	s.sweepProofs(ctx)
	t := time.NewTicker(proofRetentionInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepProofs(ctx)
		}
	}
}

// sweepProofs prunes every repo's proofs older than the retention window in one
// pass, emitting one summary event per repo that pruned something.
func (s *Server) sweepProofs(ctx context.Context) {
	cutoff, ok := retentionCutoff(time.Now(), s.proofRetention)
	if !ok {
		return
	}
	rows, err := s.stores.Proofs().ExpiredBefore(cutoff)
	if err != nil {
		logger.Verbosef("proof retention: list expired proofs: %v", err)
		return
	}
	byRepo, roots := groupByRepo(rows)
	for _, root := range roots {
		expired := byRepo[root]
		traces := s.sweepTraces(root, expired.traces)
		dropped := s.sweepBranch(ctx, root, expired.tickets)
		if traces == 0 && len(dropped) == 0 {
			continue
		}
		s.emitProofsSwept(root, traces, dropped)
	}
}

// sweepTraces deletes each expired run's recorded trace directory and clears the
// column, returning how many directories were removed from disk.
func (s *Server) sweepTraces(root string, traces map[string]string) int {
	deleted := 0
	for ticket, dir := range traces {
		removed, handled := s.deleteTrace(dir)
		if removed {
			deleted++
		}
		if handled {
			if err := s.stores.Proofs().ClearTrace(root, ticket); err != nil {
				logger.Verbosef("proof retention: clear trace %s %s: %v", root, ticket, err)
			}
		}
	}
	return deleted
}

// deleteTrace removes a recorded trace directory, guarding that it resolves inside
// the browser-harness recordings root — never an arbitrary path. handled reports
// whether the column may be cleared: true once the path is gone or was already
// absent, false when it was refused or could not be stat-ed.
func (s *Server) deleteTrace(dir string) (removed, handled bool) {
	if !traceWithinRoot(dir, s.recordingsRoot) {
		logger.Verbosef("proof retention: refusing trace outside recordings root: %q", dir)
		return false, false
	}
	_, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, true
	}
	if err != nil {
		logger.Verbosef("proof retention: stat trace %q: %v", dir, err)
		return false, false
	}
	if err := os.RemoveAll(dir); err != nil {
		logger.Verbosef("proof retention: remove trace %q: %v", dir, err)
		return false, false
	}
	return true, true
}

// sweepBranch rebuilds the repo's trau-proofs branch without the expired tickets'
// directories, returning the ones dropped. A repo with no remote or no such branch
// prunes nothing.
func (s *Server) sweepBranch(ctx context.Context, root string, tickets []string) []string {
	cfg := proofsbranch.Config{RepoDir: root, Remote: s.repoRemote(root)}
	dropped, err := proofsbranch.Prune(ctx, cfg, tickets)
	if err != nil {
		logger.Verbosef("proof retention: prune %s branch for %s: %v", proofsbranch.Branch, root, err)
		return nil
	}
	return dropped
}

func (s *Server) emitProofsSwept(root string, traces int, dropped []string) {
	fields := map[string]any{
		"traces_deleted":      traces,
		"branch_dirs_dropped": len(dropped),
	}
	if len(dropped) > 0 {
		fields["dropped_tickets"] = dropped
	}
	rows, err := s.stores.Events().Append(root, []hubstore.NewEvent{{
		TS:     time.Now().UTC().Format(time.RFC3339),
		Kind:   event.KindProofsSwept,
		Msg:    proofsSweptMessage(traces, len(dropped)),
		Fields: marshalFields(fields),
	}})
	if err != nil {
		logger.Verbosef("proof retention: emit sweep event for %s: %v", root, err)
		return
	}
	name := filepath.Base(root)
	for _, row := range rows {
		s.publishEvent(root, name, row)
	}
}

func proofsSweptMessage(traces, dropped int) string {
	return fmt.Sprintf("proofs pruned: %d trace(s), %d branch dir(s)", traces, dropped)
}

// repoRemote resolves the git remote a repo's proofs branch lives on, honoring its
// REMOTE config the way delivery did when it published them. An unreadable config
// falls back to the proofsbranch default (origin).
func (s *Server) repoRemote(root string) string {
	projectPath := config.ProjectConfigPath(root)
	userPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		userPath = config.ProjectConfigPath(home)
	}
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return ""
	}
	return cfg.Remote
}

// retentionCutoff is the RFC3339 timestamp proofs created before are expired: days
// before now, in UTC. ok is false when days is non-positive (keep forever).
func retentionCutoff(now time.Time, days int) (cutoff string, ok bool) {
	if days <= 0 {
		return "", false
	}
	return now.UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano), true
}

// traceWithinRoot reports whether traceDir resolves strictly inside root, the
// containment check the sweep requires before deleting a recorded trace. An empty
// root or trace, or the root itself, is refused.
func traceWithinRoot(traceDir, root string) bool {
	if traceDir == "" || root == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTrace, err := filepath.Abs(traceDir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(absRoot), filepath.Clean(absTrace))
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// browserRecordingsRoot resolves the directory the browser-harness recorder writes
// verify traces under, mirroring the harness's own layout:
// $BH_AGENT_WORKSPACE/recordings, else <harness-home>/agent-workspace/recordings.
func browserRecordingsRoot() string {
	if ws := os.Getenv("BH_AGENT_WORKSPACE"); ws != "" {
		return filepath.Join(expandHome(ws), "recordings")
	}
	return filepath.Join(browserHarnessHome(), "agent-workspace", "recordings")
}

// browserHarnessHome resolves the browser-harness home the same way the harness
// does: $BH_HOME, then $BROWSER_HARNESS_HOME, then $XDG_CONFIG_HOME/browser-harness,
// then ~/.config/browser-harness.
func browserHarnessHome() string {
	if h := os.Getenv("BH_HOME"); h != "" {
		return expandHome(h)
	}
	if h := os.Getenv("BROWSER_HARNESS_HOME"); h != "" {
		return expandHome(h)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(expandHome(xdg), "browser-harness")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "browser-harness")
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
		}
	}
	return path
}

// repoProofs is one repo's expired proofs: its expired trace directories keyed by
// ticket (empty-trace runs omitted) and the set of every expired ticket.
type repoProofs struct {
	traces  map[string]string
	tickets []string
}

// groupByRepo folds expired rows into per-repo sets, returning the map and its
// repo roots in stable order.
func groupByRepo(rows []hubstore.ExpiredProof) (map[string]*repoProofs, []string) {
	byRepo := map[string]*repoProofs{}
	roots := []string{}
	for _, row := range rows {
		g := byRepo[row.Repo]
		if g == nil {
			g = &repoProofs{traces: map[string]string{}}
			byRepo[row.Repo] = g
			roots = append(roots, row.Repo)
		}
		g.tickets = append(g.tickets, row.Ticket)
		if row.TraceDir != "" {
			g.traces[row.Ticket] = row.TraceDir
		}
	}
	sort.Strings(roots)
	return byRepo, roots
}
