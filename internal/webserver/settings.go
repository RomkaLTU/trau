package webserver

import (
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
	Editable    bool     `json:"editable"`
	Secret      bool     `json:"secret,omitempty"`
	Set         bool     `json:"set,omitempty"`
}

// ConfigResponse is the /api/v1/repos/{repo}/config resource: every known config
// key with its effective value and originating layer, the layers an edit may
// write to, and the providers a run may be launched with so the provider-override
// select is server-driven instead of hardcoded in the web.
type ConfigResponse struct {
	Repo      string          `json:"repo"`
	Layers    []string        `json:"layers"`
	Providers []string        `json:"providers"`
	Keys      []ConfigKeyView `json:"keys"`
}

// ConfigWriteRequest is the body of a settings edit: the key, its new value, and
// the layer to persist it to. Unset deletes the key's line from the layer's file
// to restore inheritance; Value is ignored when it is set. Saving an empty Value
// instead writes an explicit "KEY=" — the two operations stay distinct.
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
// secret values. Only the project (<repo>/.trau.ini) and user (~/.trau.ini)
// layers plus the process environment and defaults are considered — the hub's
// cwd-local file has no bearing on another repo's loop.
func (s *Server) resolveConfig(repo registry.Repo) ([]ConfigKeyView, error) {
	projectPath, userPath := s.repoConfigPaths(repo)
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
	projectPath = config.ProjectConfigPath(repo.Root)
	if home, err := os.UserHomeDir(); err == nil {
		userPath = config.ProjectConfigPath(home)
	}
	return projectPath, userPath
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
