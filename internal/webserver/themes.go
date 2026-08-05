package webserver

import (
	"net/http"
	"os"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/theme"
)

// ThemeView is one theme as the themes resource lists it. Modes names the
// polarities the theme paints; a surface asked for any other mode keeps its
// built-in palette.
type ThemeView struct {
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Author  string   `json:"author,omitempty"`
	Version string   `json:"version,omitempty"`
	Modes   []string `json:"modes"`
	Origin  string   `json:"origin"`
}

// ThemesResponse is the /api/v1/themes resource: the themes this build carries,
// which one THEME selects, and that one's palettes already resolved into the web
// variable set — so the SPA applies colors without knowing the derivation rules.
type ThemesResponse struct {
	Active   string                       `json:"active"`
	Themes   []ThemeView                  `json:"themes"`
	Resolved map[string]map[string]string `json:"resolved"`
}

func (s *Server) handleThemes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	cfg := s.themeConfig(r.URL.Query().Get("repo"))
	active, ok := theme.Lookup(cfg.Theme)
	if !ok {
		active = theme.Default()
	}
	resolved := map[string]map[string]string{}
	for _, mode := range active.DefinedModes() {
		if vars, ok := active.ResolveWeb(mode, cfg.ThemeColors); ok {
			resolved[mode] = vars
		}
	}
	bundled := theme.Bundled()
	views := make([]ThemeView, 0, len(bundled))
	for _, t := range bundled {
		views = append(views, ThemeView{
			Slug:    t.Slug,
			Name:    t.Name,
			Author:  t.Author,
			Version: t.Version,
			Modes:   t.DefinedModes(),
			Origin:  theme.OriginBundled,
		})
	}
	writeJSON(w, http.StatusOK, ThemesResponse{Active: active.Slug, Themes: views, Resolved: resolved})
}

// themeConfig resolves the config the active theme is read from. The SPA passes
// the project it is scoped to, so a repo's own THEME paints the UI while that
// repo is selected; with no project — the "All projects" scope, or a first boot
// before the repo set has loaded — only the machine baseline (~/.trau.ini) and
// the environment decide. A config that cannot be read is not an error worth
// failing a palette over: the defaults paint the built-in theme.
func (s *Server) themeConfig(repoIdent string) config.Config {
	if repoIdent != "" {
		if repo, ok := s.findRepo(repoIdent); ok {
			if cfg, err := repoConfig(repo.Root); err == nil {
				return cfg
			}
		}
	}
	userPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		userPath = config.ProjectConfigPath(home)
	}
	cfg, err := config.LoadLayered("", userPath, "", "")
	if err != nil {
		return config.Defaults()
	}
	return cfg
}
