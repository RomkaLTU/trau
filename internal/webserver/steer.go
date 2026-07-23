package webserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// SteerNoteRequest is the body of a queue call: the ticket the note steers and
// the operator's message, which may span lines.
type SteerNoteRequest struct {
	Ticket string `json:"ticket"`
	Body   string `json:"body"`
}

// SteerAckRequest is the body of an ack: the canonical phase label of the agent
// that consumed the note.
type SteerAckRequest struct {
	Phase string `json:"phase"`
}

// SteerExpireRequest is the body of an expire sweep: the ticket whose run settled.
type SteerExpireRequest struct {
	Ticket string `json:"ticket"`
}

// SteerNoteView is one steer note as every surface reads it.
type SteerNoteView struct {
	ID             int64  `json:"id"`
	Ticket         string `json:"ticket"`
	Body           string `json:"body"`
	Status         string `json:"status"`
	DeliveredPhase string `json:"delivered_phase,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	DeliveredAt    string `json:"delivered_at,omitempty"`
}

// SteerNotesResponse is a ticket's steer notes in delivery order, oldest first.
type SteerNotesResponse struct {
	Notes []SteerNoteView `json:"notes"`
}

// handleSteerNotes queues a note for a ticket (POST) or lists the ticket's notes
// (GET, ?ticket=<id>, narrowed to the undelivered ones with &status=pending).
func (s *Server) handleSteerNotes(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listSteerNotes(w, r, repo)
	case http.MethodPost:
		s.queueSteerNote(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listSteerNotes(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket is required"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != hubstore.SteerPending {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status filter must be " + hubstore.SteerPending})
		return
	}
	read := s.stores.Steer().List
	if status == hubstore.SteerPending {
		read = s.stores.Steer().Pending
	}
	notes, err := read(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list steer notes: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, steerNotesResponse(notes))
}

func (s *Server) queueSteerNote(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req SteerNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	ticket := strings.TrimSpace(req.Ticket)
	if ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket is required"})
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a steer note needs a body"})
		return
	}
	note, err := s.stores.Steer().Queue(repo.Root, ticket, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "queue steer note: " + err.Error()})
		return
	}
	s.emitSteerQueued(repo, note)
	writeJSON(w, http.StatusCreated, steerNoteView(note))
}

// handleSteerAck marks a note delivered by the phase that consumed it (POST). It
// is idempotent; a note already swept by an expire sweep conflicts.
func (s *Server) handleSteerAck(w http.ResponseWriter, r *http.Request) {
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
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "steer note id must be a number"})
		return
	}
	var req SteerAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	phase := strings.TrimSpace(req.Phase)
	if phase == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phase is required"})
		return
	}
	note, err := s.stores.Steer().Ack(repo.Root, id, phase)
	switch {
	case errors.Is(err, hubstore.ErrSteerNoteNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown steer note"})
	case errors.Is(err, hubstore.ErrSteerNoteExpired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "steer note already expired"})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ack steer note: " + err.Error()})
	default:
		writeJSON(w, http.StatusOK, steerNoteView(note))
	}
}

// handleSteerExpire sweeps a ticket's remaining pending notes (POST), the child's
// call once the run settles. Delivered notes are left alone and the sweep repeats
// harmlessly.
func (s *Server) handleSteerExpire(w http.ResponseWriter, r *http.Request) {
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
	var req SteerExpireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	ticket := strings.TrimSpace(req.Ticket)
	if ticket == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ticket is required"})
		return
	}
	expired, err := s.stores.Steer().Expire(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "expire steer notes: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, steerNotesResponse(expired))
}

// emitSteerQueued appends the queue marker to the repo's feed and fans it out
// live, the same path a child's own events take. The body stays out of the feed;
// readers fetch it from the steer API.
func (s *Server) emitSteerQueued(repo registry.Repo, note hubstore.SteerNote) {
	rows, err := s.stores.Events().Append(repo.Root, []hubstore.NewEvent{{
		TS:   time.Now().Format(time.RFC3339),
		Kind: event.KindSteerQueued,
		Msg:  "steer note queued for " + note.Ticket,
		Fields: marshalFields(map[string]any{
			"ticket":  note.Ticket,
			"note_id": note.ID,
		}),
	}})
	if err != nil {
		logger.Verbosef("steer queued event %s: %v", repo.Name, err)
		return
	}
	for _, row := range rows {
		s.publishEvent(repo.Root, repo.Name, row)
	}
}

func steerNotesResponse(notes []hubstore.SteerNote) SteerNotesResponse {
	views := make([]SteerNoteView, 0, len(notes))
	for _, n := range notes {
		views = append(views, steerNoteView(n))
	}
	return SteerNotesResponse{Notes: views}
}

func steerNoteView(n hubstore.SteerNote) SteerNoteView {
	return SteerNoteView{
		ID:             n.ID,
		Ticket:         n.Ticket,
		Body:           n.Body,
		Status:         n.Status,
		DeliveredPhase: n.DeliveredPhase,
		CreatedAt:      n.CreatedAt,
		DeliveredAt:    n.DeliveredAt,
	}
}
