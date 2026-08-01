package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
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

// ProjectRemovalRepo is one member a project-wide removal reached, with the reason
// the hub refused it on a member that stayed.
type ProjectRemovalRepo struct {
	Name   string `json:"name"`
	Root   string `json:"root"`
	Reason string `json:"reason,omitempty"`
}

// ProjectRemoval is the outcome of DELETE /api/v1/projects/{project}?forget=1.
// Project is the pre-removal snapshot, so a caller can name what it acted on even
// once the row is gone. Removed and Blocked ride back together because the removal
// is per member rather than all-or-nothing, and ProjectDeleted stays false while
// any member is blocked so those members keep their group.
type ProjectRemoval struct {
	Project        ProjectView          `json:"project"`
	Removed        []ProjectRemovalRepo `json:"removed"`
	Blocked        []ProjectRemovalRepo `json:"blocked"`
	ProjectDeleted bool                 `json:"project_deleted"`
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

// handleProject renames (PATCH) or deletes (DELETE) one project. The delete splits
// the way a repo row's does: bare drops the grouping only, ?forget=1 takes every
// member repo off the hub with it.
func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		s.renameProject(w, r)
	case http.MethodDelete:
		if r.URL.Query().Get("forget") == "1" {
			s.forgetProjectRepos(w, r)
			return
		}
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

// forgetProjectRepos takes every member repo off the hub in one call, so the folder
// that registered them clears in one action instead of one confirm per repo. Each
// member gets forgetRepo's treatment — registration, known-repos row, cached
// attachments and tracker sync state — and nothing on disk is touched. It is
// deliberately partial rather than all-or-nothing: a member the hub refuses would
// otherwise leave the project permanently un-clearable, so it stays registered
// while its siblings go, and keeps the project row with it. It follows the same
// exposure gate as registration.
func (s *Server) forgetProjectRepos(w http.ResponseWriter, r *http.Request) {
	if s.denyRegistrationIfExposed(w, "removing a project's repos") {
		return
	}
	id := r.PathValue("project")
	proj, err := s.stores.Projects().Get(id)
	if err != nil {
		writeProjectError(w, err, "failed to read project")
		return
	}
	out := ProjectRemoval{
		Project: projectView(proj),
		Removed: []ProjectRemovalRepo{},
		Blocked: []ProjectRemovalRepo{},
	}
	for _, root := range proj.Repos {
		repo, listed := s.matchListedRepo(root)
		if !listed {
			// A membership the repos list no longer answers for has nothing left to
			// remove, so dropping the row is the whole removal.
			if err := s.stores.Projects().ForgetRoot(root); err != nil {
				logger.Verbosef("drop stale project membership for %s: %v", root, err)
			}
			out.Removed = append(out.Removed, ProjectRemovalRepo{Name: filepath.Base(root), Root: root})
			continue
		}
		if reason := s.removalRefusal(repo); reason != "" {
			out.Blocked = append(out.Blocked, ProjectRemovalRepo{Name: repo.Name, Root: repo.Root, Reason: reason})
			continue
		}
		if err := s.forgetMemberRepo(repo.Root); err != nil {
			out.Blocked = append(out.Blocked, ProjectRemovalRepo{Name: repo.Name, Root: repo.Root, Reason: err.Error()})
			continue
		}
		out.Removed = append(out.Removed, ProjectRemovalRepo{Name: repo.Name, Root: repo.Root})
	}
	if len(out.Blocked) == 0 {
		// The last membership to go prunes the project row with it, so a missing row
		// here is the delete already done rather than a failure.
		if err := s.stores.Projects().Delete(id); err != nil && !errors.Is(err, hubstore.ErrProjectNotFound) {
			writeProjectError(w, err, "failed to delete project")
			return
		}
		out.ProjectDeleted = true
	}
	writeJSON(w, http.StatusOK, out)
}

// removalRefusal is why the hub keeps a member a project-wide removal aimed at,
// empty when the removal goes through. Both conflicts forgetRepo answers with a 409
// land here as a per-member reason instead, since one un-removable member must not
// strand its siblings.
func (s *Server) removalRefusal(repo registry.Repo) string {
	if _, ok := matchRoot(s.workspace, repo.Root); ok {
		return seededRepoRefusal(repo.Name, "removed")
	}
	if s.repoIsLive(repo.Root) {
		return liveLoopRefusal(repo.Name)
	}
	return ""
}

// forgetMemberRepo removes one member from the hub, mirroring forgetRepo. Only the
// registration drop can leave the repo listed, so it is the one failure the caller
// records against the member; the rest is best-effort cleanup.
func (s *Server) forgetMemberRepo(root string) error {
	if err := s.stores.Registrations().Forget(root); err != nil {
		return fmt.Errorf("failed to remove repo: %w", err)
	}
	if err := s.stores.Projects().ForgetRoot(root); err != nil {
		logger.Verbosef("drop project membership for %s: %v", root, err)
	}
	s.dropUnregisteredRepoState(root)
	return nil
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
