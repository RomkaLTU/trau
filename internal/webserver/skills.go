package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

const (
	skillsRegistryURL = "https://skills.sh/api/search"
	skillsPageBase    = "https://skills.sh/"
	skillsSearchTTL   = 60 * time.Second
)

var (
	defaultInstallSkill = agent.InstallSkillPackage
	defaultRemoveSkill  = agent.RemoveSkill
	skillsHTTPClient    = &http.Client{Timeout: 10 * time.Second}
)

// SkillView is one installed skill as the panel shows it: its directory name
// enriched with the source and pin metadata from skills-lock.json when the repo
// has a lockfile. A skill dropped in by hand carries no provenance and reads as
// unpinned.
type SkillView struct {
	Name       string `json:"name"`
	Source     string `json:"source,omitempty"`
	SourceType string `json:"source_type,omitempty"`
	SkillPath  string `json:"skill_path,omitempty"`
	Pinned     bool   `json:"pinned"`
}

// SkillRecommendationView is one recommended-but-missing starter skill for the
// repo's detected project type, with the argument to install it.
type SkillRecommendationView struct {
	Name    string `json:"name"`
	Package string `json:"package"`
	URL     string `json:"url"`
}

// SkillsResponse is the /api/v1/repos/{repo}/skills readiness snapshot: the
// detected project type, the installed skills, the recommended starters still
// missing, and the repo's pinned REQUIRED_SKILLS.
type SkillsResponse struct {
	Repo        string                    `json:"repo"`
	ProjectType string                    `json:"project_type"`
	Installed   []SkillView               `json:"installed"`
	Recommended []SkillRecommendationView `json:"recommended"`
	Required    []string                  `json:"required"`
}

// SkillInstallRequest is the body of POST /repos/{repo}/skills: the package spec
// (`owner/repo@skill`) to install through the skills.sh CLI.
type SkillInstallRequest struct {
	Package string `json:"package"`
}

// SkillSearchResult is one registry hit passed through from skills.sh, with the
// skills.sh page URL resolved from its id.
type SkillSearchResult struct {
	ID       string `json:"id"`
	SkillID  string `json:"skill_id"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
	Source   string `json:"source"`
	URL      string `json:"url"`
}

// SkillsSearchResponse is the /api/v1/repos/{repo}/skills/search resource. When
// the registry is unreachable the results are empty and Unavailable is set, so
// the panel degrades to "search is down" instead of an error.
type SkillsSearchResponse struct {
	Query       string              `json:"query"`
	Results     []SkillSearchResult `json:"results"`
	Unavailable bool                `json:"unavailable,omitempty"`
}

type skillsCacheEntry struct {
	at      time.Time
	payload SkillsSearchResponse
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.skillsSnapshot(repo))
	case http.MethodPost:
		s.installSkillHandler(w, r, repo)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) installSkillHandler(w http.ResponseWriter, r *http.Request, repo registry.Repo) {
	var req SkillInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "package is required"})
		return
	}
	if err := s.installSkill(r.Context(), repo.Root, pkg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, s.skillsSnapshot(repo))
}

func (s *Server) handleSkillItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill name is required"})
		return
	}
	if err := s.removeSkill(r.Context(), repo.Root, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.skillsSnapshot(repo))
}

func (s *Server) handleSkillsSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if _, ok := s.findRepo(r.PathValue("repo")); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if q == "" {
		writeJSON(w, http.StatusOK, SkillsSearchResponse{Query: q, Results: []SkillSearchResult{}})
		return
	}
	writeJSON(w, http.StatusOK, s.searchSkills(r.Context(), q, owner))
}

func (s *Server) skillsSnapshot(repo registry.Repo) SkillsResponse {
	readiness := agent.CheckSkillReadiness(repo.Root)
	lock := agent.ReadSkillsLock(repo.Root)

	names := agent.InstalledSkillNames(repo.Root)
	installed := make([]SkillView, 0, len(names))
	for _, name := range names {
		v := SkillView{Name: name}
		if meta, ok := lock[name]; ok {
			v.Source = meta.Source
			v.SourceType = meta.SourceType
			v.SkillPath = meta.SkillPath
			v.Pinned = true
		}
		installed = append(installed, v)
	}

	recommended := make([]SkillRecommendationView, 0, len(readiness.Missing))
	for _, rec := range readiness.Missing {
		recommended = append(recommended, SkillRecommendationView{Name: rec.Name, Package: rec.Package, URL: rec.URL})
	}

	return SkillsResponse{
		Repo:        repo.Name,
		ProjectType: readiness.ProjectType,
		Installed:   installed,
		Recommended: recommended,
		Required:    s.requiredSkills(repo),
	}
}

func (s *Server) requiredSkills(repo registry.Repo) []string {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return nil
	}
	return cfg.RequiredSkills
}

func (s *Server) searchSkills(ctx context.Context, q, owner string) SkillsSearchResponse {
	key := q + "\x00" + owner
	s.skillsMu.Lock()
	if e, ok := s.skillsCache[key]; ok && time.Since(e.at) < skillsSearchTTL {
		s.skillsMu.Unlock()
		return e.payload
	}
	s.skillsMu.Unlock()

	resp, err := fetchSkillsSearch(ctx, q, owner)
	if err != nil {
		return SkillsSearchResponse{Query: q, Results: []SkillSearchResult{}, Unavailable: true}
	}

	s.skillsMu.Lock()
	s.skillsCache[key] = skillsCacheEntry{at: time.Now(), payload: resp}
	s.skillsMu.Unlock()
	return resp
}

// fetchSkillsSearch proxies the public skills.sh registry search — the same
// unauthenticated endpoint `npx skills find` uses. The base is overridable via
// SKILLS_API_URL so a test (or an air-gapped mirror) can stand in for it, but the
// per-skill page URL always points at skills.sh.
func fetchSkillsSearch(ctx context.Context, q, owner string) (SkillsSearchResponse, error) {
	base := strings.TrimSpace(os.Getenv("SKILLS_API_URL"))
	if base == "" {
		base = skillsRegistryURL
	}
	u, err := url.Parse(base)
	if err != nil {
		return SkillsSearchResponse{}, err
	}
	query := u.Query()
	query.Set("q", q)
	query.Set("limit", "10")
	if owner != "" {
		query.Set("owner", owner)
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SkillsSearchResponse{}, err
	}
	res, err := skillsHTTPClient.Do(req)
	if err != nil {
		return SkillsSearchResponse{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return SkillsSearchResponse{}, fmt.Errorf("skills.sh returned %d", res.StatusCode)
	}

	var upstream struct {
		Skills []struct {
			ID       string `json:"id"`
			SkillID  string `json:"skillId"`
			Name     string `json:"name"`
			Installs int    `json:"installs"`
			Source   string `json:"source"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(res.Body).Decode(&upstream); err != nil {
		return SkillsSearchResponse{}, err
	}

	results := make([]SkillSearchResult, 0, len(upstream.Skills))
	for _, sk := range upstream.Skills {
		results = append(results, SkillSearchResult{
			ID:       sk.ID,
			SkillID:  sk.SkillID,
			Name:     sk.Name,
			Installs: sk.Installs,
			Source:   sk.Source,
			URL:      skillsPageBase + sk.ID,
		})
	}
	return SkillsSearchResponse{Query: q, Results: results}, nil
}
