package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

// configWriteLayers are the layers a settings edit may target, lowest to highest
// precedence. The hub never writes the cwd-local layer — it has no bearing on a
// specific repo's loop.
var configWriteLayers = []string{"project", "user"}

// globalConfigWriteLayers is the one layer a global settings edit may target:
// with no repo in scope there is no project file to write.
var globalConfigWriteLayers = []string{"user"}

// ConfigKeyView is one resolved config key as the settings surface shows it: the
// effective value, the layer it came from, and the metadata the UI needs to
// render and safely edit it. Secret keys carry no value — only whether one is
// set and which layer supplied it — so credentials never cross the wire.
type ConfigKeyView struct {
	Key         string   `json:"key"`
	Value       string   `json:"value"`
	Layer       string   `json:"layer"`
	Group       string   `json:"group"`
	Kind        string   `json:"kind,omitempty"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
	Bool        bool     `json:"bool,omitempty"`
	Advanced    bool     `json:"advanced,omitempty"`
	// Tracker lets the settings surface hide the keys of the trackers this repo
	// is not pointed at; empty is tracker-agnostic.
	Tracker  string `json:"tracker,omitempty"`
	Editable bool   `json:"editable"`
	Secret   bool   `json:"secret,omitempty"`
	Set      bool   `json:"set,omitempty"`
	// DisabledReason explains why a key the catalog marks web-editable is not
	// editable for this repo — a fact about the repo, not about the key, so it is
	// resolved per request rather than declared in the catalog.
	DisabledReason string `json:"disabled_reason,omitempty"`
}

// ConfigResponse is the /api/v1/repos/{repo}/config resource: every known config
// key with its effective value and originating layer, the layers an edit may
// write to, and the providers a run may be launched with so the provider-override
// select is server-driven instead of hardcoded in the web. /api/v1/config answers
// with the same shape for the global scope, where Repo is empty.
type ConfigResponse struct {
	Repo      string          `json:"repo"`
	Layers    []string        `json:"layers"`
	Providers []string        `json:"providers"`
	Keys      []ConfigKeyView `json:"keys"`
}

// ConfigWriteRequest is the body of a settings edit: the key, its new value, and
// the layer to persist it to. Unset deletes the key's line from the layer's file
// to restore the built-in default; Value is ignored when it is set. Saving an empty
// Value instead writes an explicit "KEY=" — the two operations stay distinct.
type ConfigWriteRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Layer string `json:"layer"`
	Unset bool   `json:"unset"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getConfig(w, repo)
	case http.MethodPut:
		s.putConfig(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleGlobalConfig serves the settings surface when no repo is in scope: the
// defaults every project inherits, resolved from the built-ins and ~/.trau.ini
// alone.
func (s *Server) handleGlobalConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getGlobalConfig(w)
	case http.MethodPut:
		s.putGlobalConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) getGlobalConfig(w http.ResponseWriter) {
	views, err := globalConfigViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load config: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ConfigResponse{Layers: globalConfigWriteLayers, Providers: agent.DefaultRegistry().Names(), Keys: views})
}

// putGlobalConfig writes a global default into ~/.trau.ini. It shares putConfig's
// gating and validation, minus the two facts about a repo rather than a key: the
// per-repo availability probe and the project tracker's claim on a seeded key.
func (s *Server) putGlobalConfig(w http.ResponseWriter, r *http.Request) {
	var req ConfigWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	key := strings.TrimSpace(req.Key)
	meta, known := knownKey(key)
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown config key %q", key)})
		return
	}
	if !meta.WebEditable {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("config key %q is read-only over the settings surface", key),
		})
		return
	}
	if req.Layer != "user" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "layer must be user"})
		return
	}

	userPath := userConfigPath()
	if req.Unset {
		if err := config.DeleteConfigLayer("user", "", "", userPath, key); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unset config: " + err.Error()})
			return
		}
	} else {
		if err := validateValue(meta, req.Value); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := config.WriteConfigLayer("user", "", "", userPath, key, req.Value); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write config: " + err.Error()})
			return
		}
	}

	views, err := globalConfigViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload config: " + err.Error()})
		return
	}
	for _, v := range views {
		if v.Key == key {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config key vanished after write"})
}

func (s *Server) getConfig(w http.ResponseWriter, repo registry.Repo) {
	views, err := s.resolveConfig(repo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load config: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ConfigResponse{Repo: repo.Name, Layers: configWriteLayers, Providers: agent.DefaultRegistry().Names(), Keys: views})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req ConfigWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	key := strings.TrimSpace(req.Key)
	meta, known := knownKey(key)
	if !known {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown config key %q", key)})
		return
	}
	if !meta.WebEditable {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("config key %q is read-only over the settings surface", key),
		})
		return
	}
	if reason, blocked := s.unavailableKeys(repo.Root)[key]; blocked {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": reason})
		return
	}
	if req.Layer != "project" && req.Layer != "user" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "layer must be project or user"})
		return
	}

	projectPath, userPath := s.repoConfigPaths(repo)
	if req.Unset {
		if err := config.DeleteConfigLayer(req.Layer, "", projectPath, userPath, key); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unset config: " + err.Error()})
			return
		}
	} else {
		if err := validateValue(meta, req.Value); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := config.WriteConfigLayer(req.Layer, "", projectPath, userPath, key, req.Value); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write config: " + err.Error()})
			return
		}
	}

	// Writing a key into the repo's own config file makes it the repo's again, so
	// the next project tracker edit no longer refreshes it.
	if req.Layer == "project" {
		if err := s.stores.Projects().ReleaseTrackerKey(repo.Root, key); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "release project tracker key: " + err.Error()})
			return
		}
	}

	views, err := s.resolveConfig(repo)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload config: " + err.Error()})
		return
	}
	for _, v := range views {
		if v.Key == key {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config key vanished after write"})
}

// resolveConfig reads the repo's layered config into browsable views, redacting
// secret values and disabling the keys this repo cannot use.
func (s *Server) resolveConfig(repo registry.Repo) ([]ConfigKeyView, error) {
	views, err := s.configViews(repo)
	if err != nil {
		return nil, err
	}
	unavailable := s.unavailableKeys(repo.Root)
	for i := range views {
		if reason, blocked := unavailable[views[i].Key]; blocked {
			views[i].Editable = false
			views[i].DisabledReason = reason
		}
	}
	return views, nil
}

// configViews resolves a repo's layered config into browsable views with secret
// values redacted. Only the project (<repo>/.trau.ini) and user (~/.trau.ini)
// layers plus the process environment and defaults are considered — the hub's
// cwd-local file has no bearing on another repo's loop. It leaves out the
// per-repo availability probe, which shells out to git, so read-only surfaces
// can resolve many repos at once.
func (s *Server) configViews(repo registry.Repo) ([]ConfigKeyView, error) {
	return layerViews(s.repoConfigPaths(repo))
}

// layerViews resolves one project/user layer pair into browsable views with
// secret values redacted. Either path may be empty, which drops that layer from
// the resolution.
func layerViews(projectPath, userPath string) ([]ConfigKeyView, error) {
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return nil, err
	}
	items, err := config.ResolveConfigItems(cfg, "", projectPath, userPath, "", config.Options{})
	if err != nil {
		return nil, err
	}
	views := make([]ConfigKeyView, 0, len(items))
	for _, it := range items {
		views = append(views, configKeyView(it))
	}
	return views, nil
}

// globalConfigViews resolves the defaults every project inherits: the built-ins
// overlaid with ~/.trau.ini and nothing else. The hub's own environment can still
// colour a value, but it is a fact about this process rather than a global
// default, so it never claims a layer of its own — a shadowed key reports the
// layer that actually holds it.
func globalConfigViews() ([]ConfigKeyView, error) {
	userPath := userConfigPath()
	views, err := layerViews("", userPath)
	if err != nil {
		return nil, err
	}
	userFile, err := config.ParseEnvFile(userPath)
	if err != nil {
		return nil, err
	}
	for i := range views {
		switch views[i].Layer {
		case string(config.LayerUser), string(config.LayerDefault):
			continue
		}
		if _, ok := userFile[views[i].Key]; ok {
			views[i].Layer = string(config.LayerUser)
		} else {
			views[i].Layer = string(config.LayerDefault)
		}
	}
	return views, nil
}

// unavailableKeys names the web-editable keys this repo cannot use, each with the
// copy explaining why. Turning one on would be inert, so the surface disables it
// and the write path refuses it.
func (s *Server) unavailableKeys(root string) map[string]string {
	out := map[string]string{}
	if reason := s.teamSyncUnavailable(context.Background(), root); reason != "" {
		out["TEAM_SYNC"] = reason
	}
	return out
}

func configKeyView(it config.ConfigItem) ConfigKeyView {
	v := ConfigKeyView{
		Key:         it.Key,
		Layer:       string(it.Layer),
		Group:       it.Group,
		Kind:        it.Kind,
		Default:     it.Default,
		Description: it.Description,
		Options:     it.Options,
		Suggestions: it.Suggestions,
		Bool:        it.Bool,
		Advanced:    it.Advanced,
		Tracker:     it.Tracker,
		Editable:    it.WebEditable,
		Secret:      config.IsSecretKey(it.Key),
	}
	if v.Secret {
		v.Set = it.Value != ""
	} else {
		v.Value = it.Value
	}
	return v
}

func (s *Server) repoConfigPaths(repo registry.Repo) (projectPath, userPath string) {
	return config.ProjectConfigPath(repo.Root), userConfigPath()
}

// userConfigPath is the machine-wide ~/.trau.ini, or empty when there is no home
// directory to resolve it against.
func userConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return config.ProjectConfigPath(home)
}

// repoConfig loads a repo's own layered config from its root alone, for the hub
// paths that hold a root rather than a registry.Repo. Like resolveConfig it
// ignores the hub's cwd-local file, which has no bearing on another repo's loop.
func repoConfig(root string) (config.Config, error) {
	return config.LoadLayered(config.ProjectConfigPath(root), userConfigPath(), "", "")
}

func knownKey(key string) (config.KeyMeta, bool) {
	for _, m := range config.KnownKeys() {
		if m.Key == key {
			return m, true
		}
	}
	return config.KeyMeta{}, false
}

// validateValue guards the write path against values a loop couldn't use. A
// secret must carry a value — clearing a credential is an unset, not an empty
// write — while an empty value on any other key is the explicit "cleared, fall
// back" write and skips the type checks. Otherwise a toggle takes 0/1, an
// enumerated key (including effort) one of its options, and an int or color key a
// value of that shape. Model keys carry only non-binding Suggestions, so a custom
// value always passes.
func validateValue(meta config.KeyMeta, value string) error {
	if config.IsSecretKey(meta.Key) {
		if value == "" {
			return fmt.Errorf("%s cannot be saved empty; unset it to remove", meta.Key)
		}
		return nil
	}
	if value == "" {
		return nil
	}
	if meta.Bool {
		if value != "0" && value != "1" {
			return fmt.Errorf("%s takes 0 or 1", meta.Key)
		}
		return nil
	}
	if len(meta.Options) > 0 {
		for _, o := range meta.Options {
			if value == o {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of: %s", meta.Key, strings.Join(meta.Options, ", "))
	}
	switch meta.Kind {
	case "int":
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%s must be a whole number", meta.Key)
		}
	case "color":
		if !isHexColor(value) {
			return fmt.Errorf("%s must be a hex color like #7D56F4", meta.Key)
		}
	}
	return nil
}

// isHexColor reports whether s is a #rrggbb hex color.
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
