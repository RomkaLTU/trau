package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
)

// ProjectTrackerKey is one key of a project's shared tracker. Secret keys carry
// no value — only whether one is stored — so credentials never cross the wire.
type ProjectTrackerKey struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
	Set    bool   `json:"set,omitempty"`
}

// ProjectTrackerResponse is the /api/v1/projects/{project}/tracker resource: the
// settings every member repo of the project shares — the tracker it talks to and
// the answers that describe how the project is worked — and the roots they are
// seeded into.
type ProjectTrackerResponse struct {
	Project string              `json:"project"`
	Repos   []string            `json:"repos"`
	Keys    []ProjectTrackerKey `json:"keys"`
}

// ProjectTrackerRequest is the body of a project tracker edit. A key left out
// keeps its stored value, as does a blank secret — so the web never has to echo
// a credential back to save an unrelated field. Any other blank value clears the
// key.
type ProjectTrackerRequest struct {
	Keys map[string]string `json:"keys"`
}

// handleProjectTracker reads (GET) or replaces (PUT) the settings a project's
// member repos share.
func (s *Server) handleProjectTracker(w http.ResponseWriter, r *http.Request) {
	proj, err := s.stores.Projects().Get(r.PathValue("project"))
	if err != nil {
		writeProjectError(w, err, "failed to read project")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := s.projectTracker(proj)
		if err != nil {
			writeTrackerReadError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, projectTrackerResponse(proj, keys))
	case http.MethodPut:
		s.putProjectTracker(w, r, proj)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) putProjectTracker(w http.ResponseWriter, r *http.Request, proj hubstore.Project) {
	var req ProjectTrackerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	allowed := config.ProjectSeededKeys()
	for key := range req.Keys {
		if !slices.Contains(allowed, key) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("%q is not a project-wide key", key),
			})
			return
		}
	}
	keys, err := s.projectTracker(proj)
	if err != nil {
		writeTrackerReadError(w, err)
		return
	}
	for _, key := range allowed {
		value, given := req.Keys[key]
		if !given {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			if !config.IsSecretKey(key) {
				delete(keys, key)
			}
			continue
		}
		if meta, known := knownKey(key); known {
			if err := validateValue(meta, value); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
		keys[key] = value
	}
	if err := s.stores.Projects().SetTracker(proj.ID, keys); err != nil {
		writeProjectError(w, err, "failed to save the project tracker")
		return
	}
	if err := s.seedProjectTracker(proj, keys); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "seed project tracker: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, projectTrackerResponse(proj, keys))
}

// projectTracker returns the tracker the project's members share. A lone repo
// fills in every project-wide key its project does not hold from its own config
// file, so a repo onboarded before projects owned the tracker keeps working with
// no prompt, the keys onboarding wrote straight to it still answer for the
// project, and a repo joining it later inherits the same answers. Adopted keys
// count as project-managed from then on, so a later edit propagates back to it.
// A project with several members was grouped deliberately and waits to be
// configured instead of claiming one member's keys.
func (s *Server) projectTracker(proj hubstore.Project) (map[string]string, error) {
	projects := s.stores.Projects()
	keys, err := projects.Tracker(proj.ID)
	if err != nil || len(proj.Repos) != 1 {
		return keys, err
	}
	root := proj.Repos[0]
	adopted, err := readRepoTracker(root)
	if err != nil {
		return keys, err
	}
	maps.DeleteFunc(adopted, func(key, _ string) bool {
		_, held := keys[key]
		return held
	})
	if len(adopted) == 0 {
		return keys, nil
	}
	seeded, err := projects.SeededTrackerKeys(root)
	if err != nil {
		return nil, err
	}
	maps.Copy(keys, adopted)
	if err := projects.SetTracker(proj.ID, keys); err != nil {
		return nil, err
	}
	for key := range adopted {
		seeded[key] = true
	}
	return keys, projects.MarkTrackerSeeded(root, slices.Collect(maps.Keys(seeded)))
}

// adoptRepoTrackers runs adoption across every project the hub holds. The
// projects list is what each screen reads and what a registration refreshes, so
// a hub upgraded over repos that predate project trackers migrates itself with
// no prompt and no manual step. It is best-effort: a repo whose config file
// cannot be read must not take the projects list down with it.
func (s *Server) adoptRepoTrackers() {
	projects, err := s.stores.Projects().List()
	if err != nil {
		logger.Verbosef("adopt project trackers: list projects: %v", err)
		return
	}
	for _, proj := range projects {
		if _, err := s.projectTracker(proj); err != nil {
			logger.Verbosef("adopt project tracker %s: %v", proj.ID, err)
		}
	}
}

func readRepoTracker(root string) (map[string]string, error) {
	path := config.ProjectConfigPath(root)
	file, err := config.ParseEnvFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	keys := map[string]string{}
	for _, key := range config.ProjectSeededKeys() {
		if value := file[key]; value != "" {
			keys[key] = value
		}
	}
	return keys, nil
}

// seedProjectTracker writes the project's tracker into every member repo's
// .trau.ini, so a CLI run straight against any member talks to the same tracker
// with no change to config layering. One unwritable repo does not strand the
// others: seeding covers every member it can and reports the ones it could not.
func (s *Server) seedProjectTracker(proj hubstore.Project, keys map[string]string) error {
	var errs []error
	for _, root := range proj.Repos {
		if err := s.seedRepoTracker(root, keys); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", root, err))
		}
	}
	return errors.Join(errs...)
}

// seedRepoTracker brings one member repo's config file in line with the project.
// A key the repo carries on its own account is left untouched — explicit
// per-repo config always outranks the project default — while a key it holds on
// the project's behalf is refreshed, or dropped once the project stops setting
// it.
func (s *Server) seedRepoTracker(root string, keys map[string]string) error {
	projects := s.stores.Projects()
	seeded, err := projects.SeededTrackerKeys(root)
	if err != nil {
		return err
	}
	path := config.ProjectConfigPath(root)
	file, err := config.ParseEnvFile(path)
	if err != nil {
		return err
	}
	write := map[string]string{}
	var drop []string
	for _, key := range config.ProjectSeededKeys() {
		value, wanted := keys[key]
		_, present := file[key]
		switch {
		case wanted && (!present || seeded[key]):
			write[key] = value
		case !wanted && present && seeded[key]:
			drop = append(drop, key)
		}
	}
	if len(write) > 0 {
		if err := config.WriteEnvFile(path, write); err != nil {
			return err
		}
	}
	for _, key := range drop {
		if err := config.DeleteEnvKey(path, key); err != nil {
			return err
		}
	}
	return projects.MarkTrackerSeeded(root, slices.Collect(maps.Keys(write)))
}

func writeTrackerReadError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read project tracker: " + err.Error()})
}

func projectTrackerResponse(proj hubstore.Project, keys map[string]string) ProjectTrackerResponse {
	resp := ProjectTrackerResponse{Project: proj.ID, Repos: proj.Repos, Keys: []ProjectTrackerKey{}}
	for _, key := range config.ProjectSeededKeys() {
		value, ok := keys[key]
		if !ok {
			continue
		}
		view := ProjectTrackerKey{Key: key, Secret: config.IsSecretKey(key)}
		if view.Secret {
			view.Set = value != ""
		} else {
			view.Value = value
		}
		resp.Keys = append(resp.Keys, view)
	}
	return resp
}
