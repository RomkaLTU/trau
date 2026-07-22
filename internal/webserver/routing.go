package webserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// routingInput is the routing fingerprint a child reports at run start, mirroring
// hubclient.RoutingFingerprint.
type routingInput struct {
	Hash string            `json:"hash"`
	Keys map[string]string `json:"keys"`
}

// RoutingResponse tells the reporting child which cohort its run landed in and
// whether recording it crossed a boundary.
type RoutingResponse struct {
	Hash    string `json:"hash"`
	Changed bool   `json:"changed"`
}

// handleRepoRouting receives the routing fingerprint a run executes under (POST).
// The hub holds the last fingerprint per repo, so it — not the child — decides
// whether the configuration changed, and emits one config_change event when it did.
func (s *Server) handleRepoRouting(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req routingInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	fp := hubstore.RoutingFingerprint{
		Hash: req.Hash,
		Keys: req.Keys,
		TS:   time.Now().Format(time.RFC3339),
	}
	changes, prevHash, err := s.stores.Routing().Observe(repo.Root, fp)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(changes) > 0 {
		s.emitConfigChange(repo, fp, prevHash, changes)
	}
	writeJSON(w, http.StatusOK, RoutingResponse{Hash: req.Hash, Changed: len(changes) > 0})
}

// emitConfigChange appends the cohort-boundary marker to the repo's feed and fans
// it out live, the same path a child's own events take.
func (s *Server) emitConfigChange(repo registry.Repo, fp hubstore.RoutingFingerprint, prevHash string, changes []hubstore.RoutingChange) {
	fields := map[string]any{
		"hash":    fp.Hash,
		"changes": routingChangeFields(changes),
	}
	if prevHash != "" {
		fields["previous_hash"] = prevHash
	}
	rows, err := s.stores.Events().Append(repo.Root, []hubstore.NewEvent{{
		TS:     fp.TS,
		Kind:   event.KindConfigChange,
		Msg:    configChangeMessage(prevHash, changes),
		Fields: marshalFields(fields),
	}})
	if err != nil {
		logger.Verbosef("config change event %s: %v", repo.Name, err)
		return
	}
	for _, row := range rows {
		s.publishEvent(repo.Root, repo.Name, row)
	}
}

// configChangeMessage names the keys that moved, so the feed line carries the
// change without a reader unfolding the fields. A repo's first fingerprint has
// every key in its diff and nothing to compare against, so it reads as a
// recording rather than a change.
func configChangeMessage(prevHash string, changes []hubstore.RoutingChange) string {
	if prevHash == "" {
		return "routing config recorded"
	}
	names := make([]string, 0, len(changes))
	for _, c := range changes {
		names = append(names, c.Key)
	}
	return "routing config changed: " + strings.Join(names, ", ")
}

func routingChangeFields(changes []hubstore.RoutingChange) []map[string]string {
	out := make([]map[string]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, map[string]string{"key": c.Key, "from": c.From, "to": c.To})
	}
	return out
}

func marshalFields(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(b)
}
