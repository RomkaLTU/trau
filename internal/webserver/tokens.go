package webserver

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
)

// tokenCallInput is one token call in an append batch, mirroring hubclient.TokenCall.
type tokenCallInput struct {
	Ticket        string   `json:"ticket"`
	TS            string   `json:"ts"`
	Phase         string   `json:"phase"`
	Input         int      `json:"input"`
	Output        int      `json:"output"`
	CacheRead     int      `json:"cache_read"`
	CacheCreation int      `json:"cache_creation"`
	Reasoning     int      `json:"reasoning"`
	Total         int      `json:"total"`
	CostUSD       *float64 `json:"cost_usd"`
	Turns         int      `json:"turns"`
	IsError       bool     `json:"is_error"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Context       int      `json:"context"`
	Skills        string   `json:"skills"`
}

// anomalyInput is one flagged anomaly in a record batch, mirroring hubclient.Anomaly.
type anomalyInput struct {
	TS      string   `json:"ts"`
	Phase   string   `json:"phase"`
	Output  int      `json:"output"`
	Turns   int      `json:"turns"`
	Cost    float64  `json:"cost_usd"`
	Reasons []string `json:"reasons"`
}

// SpendResponse is the token total the hub returns for a ticket or a day.
type SpendResponse struct {
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Metered bool    `json:"metered"`
}

// handleRepoTokens receives the loop child's token-call batch (POST). Children POST
// each provider call's usage here instead of appending a per-run log file (ADR
// 0008); the hub appends them to the authoritative token_calls table.
func (s *Server) handleRepoTokens(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Calls []tokenCallInput `json:"calls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	calls := make([]hubstore.TokenCall, len(req.Calls))
	for i, c := range req.Calls {
		calls[i] = hubstore.TokenCall{
			Ticket:        c.Ticket,
			TS:            c.TS,
			Phase:         c.Phase,
			Input:         c.Input,
			Output:        c.Output,
			CacheRead:     c.CacheRead,
			CacheCreation: c.CacheCreation,
			Reasoning:     c.Reasoning,
			Total:         c.Total,
			CostUSD:       c.CostUSD,
			Turns:         c.Turns,
			IsError:       c.IsError,
			Provider:      c.Provider,
			Model:         c.Model,
			Context:       c.Context,
			Skills:        c.Skills,
		}
	}
	if err := s.stores.Tokens().Append(repo.Root, calls); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(calls)})
}

// handleRunTokens serves a ticket's summed token + cost spend (GET) — the status
// and budget ticket-cap read.
func (s *Server) handleRunTokens(w http.ResponseWriter, r *http.Request) {
	repo, ticket, ok := s.tokenRoute(w, r, http.MethodGet)
	if !ok {
		return
	}
	sp, err := s.stores.Tokens().Total(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, spendResponse(sp))
}

// PhaseSpendView is one phase's slice of a ticket's spend in the summary.
type PhaseSpendView struct {
	Phase   string  `json:"phase"`
	Tokens  int     `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
	Turns   int     `json:"turns"`
	Calls   int     `json:"calls"`
	Metered bool    `json:"metered"`
}

// SpendSummaryResponse is a ticket's spend broken down by phase alongside the same
// grand total the status view reports — the forensics spend read.
type SpendSummaryResponse struct {
	Ticket string           `json:"ticket"`
	Total  SpendResponse    `json:"total"`
	Phases []PhaseSpendView `json:"phases"`
}

// handleRunSpend serves a ticket's spend summary (GET): the per-phase breakdown and
// the grand total, both from the authoritative token_calls table so the total
// matches the status view exactly.
func (s *Server) handleRunSpend(w http.ResponseWriter, r *http.Request) {
	repo, ticket, ok := s.tokenRoute(w, r, http.MethodGet)
	if !ok {
		return
	}
	total, err := s.stores.Tokens().Total(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	phases, err := s.stores.Tokens().PhaseTotals(repo.Root, ticket)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := SpendSummaryResponse{Ticket: ticket, Total: spendResponse(total), Phases: make([]PhaseSpendView, 0, len(phases))}
	for _, p := range phases {
		out.Phases = append(out.Phases, PhaseSpendView{
			Phase:   p.Phase,
			Tokens:  p.Total,
			CostUSD: p.Cost,
			Turns:   p.Turns,
			Calls:   p.Calls,
			Metered: p.Metered,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTokenDay serves repo's summed spend for the ?date= local date, defaulting
// to today — the budget day-cap read.
func (s *Server) handleTokenDay(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format(dateLayout)
	}
	sp, err := s.stores.Tokens().DayTotal(repo.Root, date)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, spendResponse(sp))
}

// handleRunAnomalies records a ticket's flagged cost anomalies (POST), replacing any
// the hub already holds for it. Children POST anomalies here instead of writing a
// per-run anomaly log file (ADR 0008).
func (s *Server) handleRunAnomalies(w http.ResponseWriter, r *http.Request) {
	repo, ticket, ok := s.tokenRoute(w, r, http.MethodPost)
	if !ok {
		return
	}
	var req struct {
		Anomalies []anomalyInput `json:"anomalies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	anomalies := make([]hubstore.Anomaly, len(req.Anomalies))
	for i, a := range req.Anomalies {
		anomalies[i] = hubstore.Anomaly{
			TS:      a.TS,
			Phase:   a.Phase,
			Output:  a.Output,
			Turns:   a.Turns,
			Cost:    a.Cost,
			Reasons: a.Reasons,
		}
	}
	if err := s.stores.Tokens().RecordAnomalies(repo.Root, ticket, anomalies); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(anomalies)})
}

// tokenRoute resolves the {repo} and {ticket} segments and enforces the method for
// the per-run token endpoints, writing the error response itself on any miss.
func (s *Server) tokenRoute(w http.ResponseWriter, r *http.Request, method string) (registry.Repo, string, bool) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return registry.Repo{}, "", false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return registry.Repo{}, "", false
	}
	return repo, r.PathValue("ticket"), true
}

func spendResponse(sp hubstore.Spend) SpendResponse {
	return SpendResponse{Tokens: sp.Tokens, CostUSD: sp.Cost, Metered: sp.Metered}
}
