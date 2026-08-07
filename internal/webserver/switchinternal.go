package webserver

import (
	"errors"
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

// trackerSwitchBusy is the refusal the busy guard raises: live external queue
// work under an identifier the drop would retire. It travels as an error so every
// caller of the guarded drop answers with the one structured body.
type trackerSwitchBusy struct {
	Blockers []TrackerSwitchBlocker
}

func (e *trackerSwitchBusy) Error() string { return blockedSwitchMessage(e.Blockers) }

// switchesToInternal reports whether a settings write points a repo at the
// internal tracker.
func switchesToInternal(key, value string) bool {
	return key == "TRACKER_PROVIDER" && strings.EqualFold(strings.TrimSpace(value), internalProvider)
}

// guardSwitchToInternal runs the migration a switch to the internal tracker owes
// the roots it covers, and reports whether the caller may go on to write the key
// (ADR 0042). A refusal or a failure is written to w.
func (s *Server) guardSwitchToInternal(w http.ResponseWriter, roots []string) bool {
	if _, err := s.dropInternalMirrors(roots); err != nil {
		writeInternalDropErr(w, err)
		return false
	}
	return true
}

// dropInternalMirrors retires the mirrors the roots it covers hold on an external
// tracker's behalf and returns how many issues it dropped (ADR 0042). A repo still
// mirroring has that mirror dropped — internal issues, the cached binding and the
// tracker credentials all stay — after its identifier sequence is lifted clear of
// every id the mirror and the queue history spoke for.
//
// Live external queue work refuses the whole migration before anything is
// dropped, as a *trackerSwitchBusy, so a half-migrated project is never left
// behind. Nothing external is ever notified, which is what makes the switch
// reversible.
func (s *Server) dropInternalMirrors(roots []string) (int, error) {
	store := s.stores.Issues()
	var mirrored []registry.Repo
	cleared := 0
	for _, root := range roots {
		synced, err := store.CountSynced(root)
		if err != nil {
			return 0, fmt.Errorf("read synced issues: %w", err)
		}
		if synced == 0 {
			continue
		}
		cleared += synced
		repo, ok := s.findRepoByRoot(root)
		if !ok {
			repo = workspaceRepo(root)
		}
		mirrored = append(mirrored, repo)
	}
	if len(mirrored) == 0 {
		return 0, nil
	}

	blockers, err := s.trackerSwitchBlockers(roots)
	if err != nil {
		return 0, fmt.Errorf("read queue: %w", err)
	}
	if len(blockers) > 0 {
		return 0, &trackerSwitchBusy{Blockers: blockers}
	}

	for _, repo := range mirrored {
		if _, err := store.RaiseInternalSeq(repo.Root, s.internalPrefix(repo)); err != nil {
			return 0, fmt.Errorf("advance the internal issue sequence for %s: %w", repo.Root, err)
		}
		if err := store.DropSynced(repo.Root); err != nil {
			return 0, fmt.Errorf("drop the synced mirror of %s: %w", repo.Root, err)
		}
	}
	return cleared, nil
}

// writeInternalDropErr maps a refused or failed mirror drop onto its response: the
// busy guard answers 409 with the blocking ids named, so a client can tell "settle
// this ticket first" from "the hub could not do it".
func writeInternalDropErr(w http.ResponseWriter, err error) {
	var busy *trackerSwitchBusy
	if errors.As(err, &busy) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   busy.Error(),
			"reason":  "queue_busy",
			"blocked": busy.Blockers,
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
