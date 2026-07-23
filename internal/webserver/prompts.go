package webserver

import (
	"encoding/json"
	"net/http"

	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/registry"
)

// PromptPlaceholderView documents one placeholder an override template may
// reference.
type PromptPlaceholderView struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptView is one prompt-catalog entry for the global scope: the registry
// metadata, the built-in default body, and the global override (null when
// unset).
type PromptView struct {
	Name         string                  `json:"name"`
	Title        string                  `json:"title"`
	Description  string                  `json:"description"`
	Placeholders []PromptPlaceholderView `json:"placeholders"`
	Default      string                  `json:"default"`
	Override     *string                 `json:"override"`
}

// RepoPromptView is the repo-scoped catalog entry: the global view plus the
// repo override (null when unset), which scope the effective body comes from,
// and the effective body itself after repo > global > default precedence.
type RepoPromptView struct {
	PromptView
	RepoOverride  *string `json:"repo_override"`
	Effective     string  `json:"effective"`
	EffectiveBody string  `json:"effective_body"`
}

// PromptsResponse is the /api/v1/prompts resource.
type PromptsResponse struct {
	Prompts []PromptView `json:"prompts"`
}

// RepoPromptsResponse is the /api/v1/repos/{repo}/prompts resource.
type RepoPromptsResponse struct {
	Repo    string           `json:"repo"`
	Prompts []RepoPromptView `json:"prompts"`
}

// PromptWriteRequest is the body of an override save.
type PromptWriteRequest struct {
	Body string `json:"body"`
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	overrides, err := s.stores.Prompts().Scope("")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load prompt overrides: " + err.Error()})
		return
	}
	catalog := prompts.Catalog()
	views := make([]PromptView, 0, len(catalog))
	for _, p := range catalog {
		views = append(views, promptView(p, overrides))
	}
	writeJSON(w, http.StatusOK, PromptsResponse{Prompts: views})
}

func (s *Server) handlePromptItem(w http.ResponseWriter, r *http.Request) {
	p, ok := prompts.Lookup(r.PathValue("name"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown prompt"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !s.putPromptOverride(w, r, p, "") {
			return
		}
		overrides, err := s.stores.Prompts().Scope("")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load prompt overrides: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, promptView(p, overrides))
	case http.MethodDelete:
		if err := s.stores.Prompts().Delete(p.Name, ""); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reset prompt override: " + err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleRepoPrompts(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	views, err := s.repoPromptViews(repo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load prompt overrides: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, RepoPromptsResponse{Repo: repo.Name, Prompts: views})
}

func (s *Server) handleRepoPromptItem(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	p, ok := prompts.Lookup(r.PathValue("name"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown prompt"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		if !s.putPromptOverride(w, r, p, repo.Root) {
			return
		}
		global, scoped, effective, err := s.promptScopes(repo.Root)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load prompt overrides: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, repoPromptView(p, global, scoped, effective))
	case http.MethodDelete:
		if err := s.stores.Prompts().Delete(p.Name, repo.Root); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reset prompt override: " + err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// putPromptOverride decodes, validates, and stores an override, reporting
// whether it was saved. A validation failure is a 422 whose message the UI
// shows verbatim; nothing is stored for a body that fails validation.
func (s *Server) putPromptOverride(w http.ResponseWriter, r *http.Request, p prompts.Prompt, scope string) bool {
	var req PromptWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	if err := p.ValidateOverride(req.Body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return false
	}
	if err := s.stores.Prompts().Set(p.Name, scope, req.Body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save prompt override: " + err.Error()})
		return false
	}
	return true
}

// promptRenderer resolves the prompt catalog for root — repo override over
// global override over built-in default — into a renderer. A store failure or an
// override that will not render falls back to the built-in defaults rather than
// failing the turn.
func (s *Server) promptRenderer(root string) prompts.Renderer {
	effective, err := s.stores.Prompts().Effective(root)
	if err != nil {
		logger.Verbosef("prompt overrides unavailable for %s: %v", root, err)
		return prompts.Renderer{}
	}
	return prompts.Renderer{
		Overrides: effective,
		OnOverrideError: func(name string, err error) {
			logger.Verbosef("prompt override %q failed to render — using the built-in default: %v", name, err)
		},
	}
}

func (s *Server) repoPromptViews(repo registry.Repo) ([]RepoPromptView, error) {
	global, scoped, effective, err := s.promptScopes(repo.Root)
	if err != nil {
		return nil, err
	}
	catalog := prompts.Catalog()
	views := make([]RepoPromptView, 0, len(catalog))
	for _, p := range catalog {
		views = append(views, repoPromptView(p, global, scoped, effective))
	}
	return views, nil
}

func (s *Server) promptScopes(root string) (global, scoped, effective map[string]string, err error) {
	store := s.stores.Prompts()
	if global, err = store.Scope(""); err != nil {
		return nil, nil, nil, err
	}
	if scoped, err = store.Scope(root); err != nil {
		return nil, nil, nil, err
	}
	if effective, err = store.Effective(root); err != nil {
		return nil, nil, nil, err
	}
	return global, scoped, effective, nil
}

func promptView(p prompts.Prompt, overrides map[string]string) PromptView {
	phs := make([]PromptPlaceholderView, 0, len(p.Placeholders))
	for _, ph := range p.Placeholders {
		phs = append(phs, PromptPlaceholderView{Name: ph.Field, Description: ph.Description, Required: ph.Required})
	}
	return PromptView{
		Name:         p.Name,
		Title:        p.Title,
		Description:  p.Description,
		Placeholders: phs,
		Default:      p.Default,
		Override:     overrideBody(overrides, p.Name),
	}
}

func repoPromptView(p prompts.Prompt, global, scoped, effective map[string]string) RepoPromptView {
	v := RepoPromptView{
		PromptView:    promptView(p, global),
		RepoOverride:  overrideBody(scoped, p.Name),
		Effective:     "default",
		EffectiveBody: effective[p.Name],
	}
	if _, ok := global[p.Name]; ok {
		v.Effective = "global"
	}
	if _, ok := scoped[p.Name]; ok {
		v.Effective = "repo"
	}
	return v
}

func overrideBody(m map[string]string, name string) *string {
	if body, ok := m[name]; ok {
		return &body
	}
	return nil
}
