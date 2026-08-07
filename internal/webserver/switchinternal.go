package webserver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/RomkaLTU/trau/internal/registry"
)

// internalProvider is the TRACKER_PROVIDER value that means "no external tracker,
// the hub holds the issues".
const internalProvider = "internal"

// TrackerSwitchBlocker is one queue entry that refuses a switch to the internal
// tracker.
type TrackerSwitchBlocker struct {
	Repo   string `json:"repo"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

// switchesToInternal reports whether a settings write points a repo at the
// internal tracker.
func switchesToInternal(key, value string) bool {
	return key == "TRACKER_PROVIDER" && strings.EqualFold(strings.TrimSpace(value), internalProvider)
}

// guardSwitchToInternal runs the migration a switch to the internal tracker owes
// the roots it covers, and reports whether the caller may go on to write the key
// (ADR 0042). A repo still mirroring an external tracker has that mirror dropped
// — internal issues, the cached binding and the tracker credentials all stay —
// after its identifier sequence is lifted clear of every id the mirror and the
// queue history spoke for.
//
// Live external queue work refuses the whole write before anything is dropped, so
// a half-migrated project is never left behind. A refusal or a failure is written
// to w. Nothing external is ever notified, which is what makes the switch
// reversible.
func (s *Server) guardSwitchToInternal(w http.ResponseWriter, roots []string) bool {
	store := s.stores.Issues()
	var mirrored []registry.Repo
	for _, root := range roots {
		synced, err := store.HasSynced(root)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read synced issues: " + err.Error()})
			return false
		}
		if !synced {
			continue
		}
		repo, ok := s.findRepoByRoot(root)
		if !ok {
			repo = workspaceRepo(root)
		}
		mirrored = append(mirrored, repo)
	}
	if len(mirrored) == 0 {
		return true
	}

	blockers, err := s.trackerSwitchBlockers(roots)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read queue: " + err.Error()})
		return false
	}
	if len(blockers) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   blockedSwitchMessage(blockers),
			"reason":  "queue_busy",
			"blocked": blockers,
		})
		return false
	}

	for _, repo := range mirrored {
		if _, err := store.RaiseInternalSeq(repo.Root, s.internalPrefix(repo)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "advance the internal issue sequence for " + repo.Root + ": " + err.Error(),
			})
			return false
		}
		if err := store.DropSynced(repo.Root); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "drop the synced mirror of " + repo.Root + ": " + err.Error(),
			})
			return false
		}
	}
	return true
}

// trackerSwitchBlockers collects the live external queue entry each root still
// holds. Every root the write covers is checked, not only the ones carrying a
// mirror.
func (s *Server) trackerSwitchBlockers(roots []string) ([]TrackerSwitchBlocker, error) {
	store := s.stores.Issues()
	var blockers []TrackerSwitchBlocker
	for _, root := range roots {
		busy, err := store.BusyExternalItem(root)
		if err != nil {
			return nil, err
		}
		if busy == nil {
			continue
		}
		blockers = append(blockers, TrackerSwitchBlocker{Repo: root, ID: busy.Identifier, Status: busy.Status})
	}
	return blockers, nil
}

// blockedSwitchMessage names the tickets holding the switch up.
func blockedSwitchMessage(blockers []TrackerSwitchBlocker) string {
	held := make([]string, 0, len(blockers))
	for _, b := range blockers {
		held = append(held, fmt.Sprintf("%s (%s)", b.ID, b.Status))
	}
	return fmt.Sprintf(
		"cannot switch to the internal tracker while the queue still holds tracker work: %s — settle or remove %s first",
		strings.Join(held, ", "), pluralIssues(len(blockers)),
	)
}

func pluralIssues(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
