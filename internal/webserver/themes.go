package webserver

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/theme"
)

// resolveSlug is the one name the themes routes claim for themselves:
// /themes/resolve is the draft resolver, so a theme saved under it could never
// be exported or deleted through the API.
const resolveSlug = "resolve"

// ThemeView is one theme as the themes resource lists it. Modes names the
// polarities the theme paints; a surface asked for any other mode keeps its
// built-in palette. Roles carries each defined mode's authored role colors, so a
// picker can paint every theme's swatch without resolving a palette per theme.
type ThemeView struct {
	Slug    string                       `json:"slug"`
	Name    string                       `json:"name"`
	Author  string                       `json:"author,omitempty"`
	Version string                       `json:"version,omitempty"`
	Modes   []string                     `json:"modes"`
	Roles   map[string]map[string]string `json:"roles"`
	Origin  string                       `json:"origin"`
}

// ThemesResponse is the /api/v1/themes resource: the themes this machine
// carries — bundled plus saved — which one THEME selects, and that one's
// palettes already resolved into the web variable set, so the SPA applies colors
// without knowing the derivation rules. Note is set when THEME names a theme
// that is no longer installed and the default took over.
type ThemesResponse struct {
	Active   string                       `json:"active"`
	Themes   []ThemeView                  `json:"themes"`
	Resolved map[string]map[string]string `json:"resolved"`
	Note     string                       `json:"note,omitempty"`
}

// ThemeSaveResponse answers a save. Replaced tells the editor whether the slug
// took over a theme that was already saved.
type ThemeSaveResponse struct {
	Theme    ThemeView `json:"theme"`
	Replaced bool      `json:"replaced"`
}

// ThemeResolveResponse is a draft theme's resolved variable sets, one per mode
// the draft defines. The editor previews from it, which keeps the derivation
// table in Go rather than reimplemented in the browser.
type ThemeResolveResponse struct {
	Slug     string                       `json:"slug"`
	Modes    []string                     `json:"modes"`
	Resolved map[string]map[string]string `json:"resolved"`
}

func (s *Server) handleThemes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listThemes(w, r)
	case http.MethodPost:
		s.saveTheme(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// handleTheme reads one theme as its canonical file (GET) or deletes a saved one
// (DELETE). The GET body is exactly what the editor exports and what a user may
// drop back into the themes directory, so duplicating a theme keeps the vars and
// tui blocks the format lets an author pin.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	slug := theme.Normalize(r.PathValue("slug"))
	switch r.Method {
	case http.MethodGet:
		s.getTheme(w, slug)
	case http.MethodDelete:
		s.deleteTheme(w, slug)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listThemes(w http.ResponseWriter, r *http.Request) {
	cfg := s.themeConfig(r.URL.Query().Get("repo"))
	local := s.localThemes()
	views := append(themeViews(theme.Bundled(), theme.OriginBundled), themeViews(local, theme.OriginLocal)...)

	active, ok := theme.LookupIn(s.themeDir(), cfg.Theme)
	note := ""
	if !ok {
		active = theme.Default()
		note = fmt.Sprintf("THEME=%s names no theme on this machine — the %s theme applies.",
			theme.Normalize(cfg.Theme), theme.DefaultSlug)
	}
	resolved := map[string]map[string]string{}
	for _, mode := range active.DefinedModes() {
		if vars, ok := active.ResolveWeb(mode, cfg.ThemeColors); ok {
			resolved[mode] = vars
		}
	}
	writeJSON(w, http.StatusOK, ThemesResponse{
		Active:   active.Slug,
		Themes:   views,
		Resolved: resolved,
		Note:     note,
	})
}

// saveTheme installs a theme file under the trau home. It is a cosmetic write:
// the API's bearer gating already covers it, and unlike repo registration it
// widens nothing, so it takes no second gate.
func (s *Server) saveTheme(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeThemeBody(w, r)
	if !ok {
		return
	}
	if _, reserved := theme.LookupBundled(t.Slug); reserved {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("theme: slug: %q is a predefined theme name and cannot be replaced", t.Slug),
		})
		return
	}
	if t.Slug == resolveSlug {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("theme: slug: %q is reserved by the draft resolver", t.Slug),
		})
		return
	}
	dir := s.themeDir()
	replaced, err := theme.SaveLocal(dir, t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save theme: " + err.Error()})
		return
	}
	status := http.StatusCreated
	if replaced {
		status = http.StatusOK
	}
	writeJSON(w, status, ThemeSaveResponse{
		Theme:    themeView(t, theme.OriginLocal),
		Replaced: replaced,
	})
}

func (s *Server) getTheme(w http.ResponseWriter, slug string) {
	t, ok := theme.LookupIn(s.themeDir(), slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown theme"})
		return
	}
	data, err := theme.Canonical(t)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render theme: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) deleteTheme(w http.ResponseWriter, slug string) {
	if _, bundled := theme.LookupBundled(slug); bundled {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("theme: %q ships with trau and cannot be deleted", slug),
		})
		return
	}
	removed, err := theme.DeleteLocal(s.themeDir(), slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete theme: " + err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown theme"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug})
}

// handleThemeResolve expands a draft theme into its per-mode variable sets. The
// draft is resolved as authored — THEME_<ROLE> overrides stay out of it — so the
// editor previews the theme the user is writing rather than this repo's
// resolution of it, which is the same rule the picker's swatches follow.
func (s *Server) handleThemeResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	t, ok := decodeThemeBody(w, r)
	if !ok {
		return
	}
	modes := t.DefinedModes()
	resolved := make(map[string]map[string]string, len(modes))
	for _, mode := range modes {
		if vars, ok := t.ResolveWeb(mode, nil); ok {
			resolved[mode] = vars
		}
	}
	writeJSON(w, http.StatusOK, ThemeResolveResponse{Slug: t.Slug, Modes: modes, Resolved: resolved})
}

// decodeThemeBody reads a theme document from the request under the format's own
// size cap, so an oversized or malformed draft is refused with the field-level
// message the format produces.
func decodeThemeBody(w http.ResponseWriter, r *http.Request) (theme.Theme, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, theme.MaxFileBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": fmt.Sprintf("theme: body is over the %d byte cap", theme.MaxFileBytes),
			})
			return theme.Theme{}, false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read theme: " + err.Error()})
		return theme.Theme{}, false
	}
	t, err := theme.Parse(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return theme.Theme{}, false
	}
	return t, true
}

// themeDir is where this hub keeps saved themes. It hangs off the hub's own trau
// home rather than the process environment, so a hub started against an isolated
// home never reads or writes the machine's themes.
func (s *Server) themeDir() string {
	return theme.LocalDir(s.home)
}

// localThemes loads the saved set. A file that no longer parses is skipped with
// a logged note rather than failing the listing.
func (s *Server) localThemes() []theme.Theme {
	themes, skipped := theme.LoadLocal(s.themeDir())
	for _, skip := range skipped {
		logger.Verbosef("themes: skip %s: %s", skip.File, skip.Reason)
	}
	return themes
}

func themeViews(themes []theme.Theme, origin string) []ThemeView {
	out := make([]ThemeView, 0, len(themes))
	for _, t := range themes {
		out = append(out, themeView(t, origin))
	}
	return out
}

func themeView(t theme.Theme, origin string) ThemeView {
	modes := t.DefinedModes()
	roles := make(map[string]map[string]string, len(modes))
	for _, mode := range modes {
		roles[mode], _ = t.ModeRoles(mode)
	}
	return ThemeView{
		Slug:    t.Slug,
		Name:    t.Name,
		Author:  t.Author,
		Version: t.Version,
		Modes:   modes,
		Roles:   roles,
		Origin:  origin,
	}
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
