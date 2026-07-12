package hubstore

import (
	"path/filepath"
	"sort"
	"strings"
)

// legacyAgentResultsDir holds the ephemeral agent-interface files (pty logs,
// .result.json) exempt under ADR 0008 §6 — never treated as leftover run data.
const legacyAgentResultsDir = "_agent-results"

// LegacyRunDataFiles returns the file-era run-data files still present under
// runsDir — per-ticket state, phase logs, phase artifacts, the lessons ledger,
// and orphaned event/token streams. The hub folds the importable ones into the
// databases on first serve touch (ADR 0008); a non-empty result is what `trau
// doctor` surfaces as an unmigrated install. Exempt agent-interface files under
// _agent-results are excluded.
func LegacyRunDataFiles(runsDir string) []string {
	if runsDir == "" {
		return nil
	}
	patterns := []string{
		filepath.Join(runsDir, "*", "state"),
		filepath.Join(runsDir, "*", "*.log"),
		filepath.Join(runsDir, "*", "handoff.md"),
		filepath.Join(runsDir, "*", "rubric.json"),
		filepath.Join(runsDir, "*", "verdict.json"),
		filepath.Join(runsDir, "*", "buildnotes.md"),
		filepath.Join(runsDir, "*", "events.jsonl"),
		filepath.Join(runsDir, "*", "tokens.jsonl"),
		filepath.Join(runsDir, "events.jsonl"),
		filepath.Join(runsDir, "memory", "lessons.jsonl"),
	}
	exempt := string(filepath.Separator) + legacyAgentResultsDir + string(filepath.Separator)
	seen := map[string]bool{}
	var found []string
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			if seen[m] || strings.Contains(m, exempt) {
				continue
			}
			seen[m] = true
			found = append(found, m)
		}
	}
	sort.Strings(found)
	return found
}
