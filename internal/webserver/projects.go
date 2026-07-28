package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/RomkaLTU/trau/internal/hubstore"
)

// ProjectView is one project as the hub serves it: its identifier, display name,
// and member repo roots in order. It carries roots rather than repos so the repos
// resource stays the single source of truth for what each root is.
type ProjectView struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Repos []string `json:"repos"`
}

// ProjectsResponse is the /api/v1/projects resource: every project the hub holds,
// ordered by display name.
type ProjectsResponse struct {
	Projects []ProjectView `json:"projects"`
}

// ProjectRequest is the body of the project create and rename calls.
type ProjectRequest struct {
	Name string `json:"name"`
}

// ProjectRepoRequest is the body of POST /api/v1/projects/{project}/repos: an
// already-registered repo addressed by name or root.
type ProjectRepoRequest struct {
	Repo string `json:"repo"`
}

// handleProjects lists the projects (GET) or creates one (POST).
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.adoptRepoTrackers()
		projects, err := s.stores.Projects().List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list projects: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, ProjectsResponse{Projects: projectViews(projects)})
	case http.MethodPost:
		s.createProject(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleProject renames (PATCH) or deletes (DELETE) one project.
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		s.renameProject(w, r)
	case http.MethodDelete:
		s.deleteProject(w, r)
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// createProject files a new, empty project under the requested name. Grouping
// cannot widen the startable set, but the write follows the registration gate all
// the same, keeping every write that reshapes the repo list under one rule.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "creating a project") {
		return
	}
	var req ProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	proj, err := s.stores.Projects().Create(req.Name)
	if err != nil {
		writeProjectError(w, err, "failed to create project")
		return
	}
	writeJSON(w, http.StatusCreated, projectView(proj))
}

func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "renaming a project") {
		return
	}
	var req ProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	proj, err := s.stores.Projects().Rename(r.PathValue("project"), req.Name)
	if err != nil {
		writeProjectError(w, err, "failed to rename project")
		return
	}
	writeJSON(w, http.StatusOK, projectView(proj))
}

// deleteProject drops the grouping only; its member repos stay registered.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "deleting a project") {
		return
	}
	id := r.PathValue("project")
	proj, err := s.stores.Projects().Get(id)
	if err != nil {
		writeProjectError(w, err, "failed to read project")
		return
	}
	if err := s.stores.Projects().Delete(id); err != nil {
		writeProjectError(w, err, "failed to delete project")
		return
	}
	writeJSON(w, http.StatusOK, projectView(proj))
}

// handleProjectRepos adds an already-registered repo to a project (POST), moving
// it out of whichever project held it.
func (s *Server) handleProjectRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "adding a repo to a project") {
		return
	}
	var req ProjectRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	repo, ok := s.findRepo(req.Repo)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("repo %q is not known to the hub", req.Repo)})
		return
	}
	proj, err := s.stores.Projects().AddRepo(r.PathValue("project"), repo.Root)
	if err != nil {
		writeProjectError(w, err, "failed to add repo to project")
		return
	}
	// The stored tracker, not projectTracker: a project being assembled repo by
	// repo is momentarily single-member, and adopting there would claim the first
	// joiner's own keys as the project default.
	keys, err := s.stores.Projects().Tracker(proj.ID)
	if err == nil {
		err = s.seedRepoTracker(repo.Root, keys)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("repo %q joined the project but seeding its tracker failed: %v", repo.Name, err),
		})
		return
	}
	writeJSON(w, http.StatusOK, projectView(proj))
}

// handleProjectRepo removes a repo from a project (DELETE), leaving it registered
// and standalone.
func (s *Server) handleProjectRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "removing a repo from a project") {
		return
	}
	ident := r.PathValue("repo")
	repo, ok := s.findRepo(ident)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("repo %q is not known to the hub", ident)})
		return
	}
	proj, err := s.stores.Projects().RemoveRepo(r.PathValue("project"), repo.Root)
	if err != nil {
		writeProjectError(w, err, "failed to remove repo from project")
		return
	}
	writeJSON(w, http.StatusOK, projectView(proj))
}

// writeProjectError maps the store's sentinels onto their status codes.
func writeProjectError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, hubstore.ErrProjectNotFound), errors.Is(err, hubstore.ErrProjectRepoNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, hubstore.ErrProjectNameEmpty):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": action + ": " + err.Error()})
	}
}

func projectViews(projects []hubstore.Project) []ProjectView {
	views := make([]ProjectView, 0, len(projects))
	for _, proj := range projects {
		views = append(views, projectView(proj))
	}
	return views
}

func projectView(proj hubstore.Project) ProjectView {
	repos := proj.Repos
	if repos == nil {
		repos = []string{}
	}
	return ProjectView{ID: proj.ID, Name: proj.Name, Repos: repos}
}
