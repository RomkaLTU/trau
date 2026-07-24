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
	"github.com/RomkaLTU/trau/internal/skillrules"
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
// enriched with its SKILL.md frontmatter, the routing scope in force for it, and
// the source and pin metadata from skills-lock.json when the repo has a
// lockfile. A skill dropped in by hand carries no provenance and reads as
// unpinned; one whose SKILL.md is missing or unreadable reads as invalid, so a
// broken install is visible instead of counting as healthy.
type SkillView struct {
	Name              string   `json:"name"`
	DeclaredName      string   `json:"declared_name,omitempty"`
	Description       string   `json:"description,omitempty"`
	SuggestedKeywords []string `json:"suggested_keywords,omitempty"`
	Invalid           bool     `json:"invalid,omitempty"`
	Scope             string   `json:"scope"`
	Source            string   `json:"source,omitempty"`
	SourceType        string   `json:"source_type,omitempty"`
	SkillPath         string   `json:"skill_path,omitempty"`
	Pinned            bool     `json:"pinned"`
}

// SkillRuleView is one repo-owned routing rule as the panel edits it.
type SkillRuleView struct {
	Skill    string   `json:"skill"`
	Scope    string   `json:"scope"`
	Phases   []string `json:"phases,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// SkillPlanView is the set one phase would name for a run with no ticket and no
// diff — every auto rule reads as non-matching, so the plan shows the repo's
// floor rather than any particular slice's set.
type SkillPlanView struct {
	Phase  string   `json:"phase"`
	Skills []string `json:"skills"`
	Source string   `json:"source"`
}

// SkillRulesRequest is the body of PUT /repos/{repo}/skills/rules: the repo's
// whole rule list, replacing whatever the rules file held.
type SkillRulesRequest struct {
	Rules []SkillRuleView `json:"rules"`
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
// missing, the repo's pinned REQUIRED_SKILLS, its routing rules, and the set
// each phase would resolve to under them. Unknown names the rules point at a
// skill the repo cannot load; RulesError reports a rules file that would not
// parse, which leaves every phase on the fallback chain.
type SkillsResponse struct {
	Repo        string                    `json:"repo"`
	ProjectType string                    `json:"project_type"`
	Installed   []SkillView               `json:"installed"`
	Recommended []SkillRecommendationView `json:"recommended"`
	Required    []string                  `json:"required"`
	Rules       []SkillRuleView           `json:"rules"`
	Plan        []SkillPlanView           `json:"plan"`
	Unknown     []string                  `json:"unknown,omitempty"`
	RulesError  string                    `json:"rules_error,omitempty"`
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
	required, requiredVerify := s.requiredSkills(repo)
	resolver := agent.NewSkillResolver(repo.Root, required, requiredVerify)
	scopes := ruleScopes(resolver.Rules())

	metas := agent.InstalledSkills(repo.Root)
	installed := make([]SkillView, 0, len(metas))
	for _, meta := range metas {
		v := SkillView{
			Name:              meta.Name,
			DeclaredName:      meta.DeclaredName,
			Description:       meta.Description,
			SuggestedKeywords: agent.SuggestedKeywords(meta.Description),
			Invalid:           meta.Invalid,
			Scope:             scopes[meta.Name],
		}
		if lockMeta, ok := lock[meta.Name]; ok {
			v.Source = lockMeta.Source
			v.SourceType = lockMeta.SourceType
			v.SkillPath = lockMeta.SkillPath
			v.Pinned = true
		}
		installed = append(installed, v)
	}

	recommended := make([]SkillRecommendationView, 0, len(readiness.Missing))
	for _, rec := range readiness.Missing {
		recommended = append(recommended, SkillRecommendationView{Name: rec.Name, Package: rec.Package, URL: rec.URL})
	}

	resp := SkillsResponse{
		Repo:        repo.Name,
		ProjectType: readiness.ProjectType,
		Installed:   installed,
		Recommended: recommended,
		Required:    required,
		Rules:       ruleViews(resolver.Rules()),
		Plan:        skillPlan(resolver),
		Unknown:     resolver.UnknownRuleSkills(),
	}
	if err := resolver.RulesError(); err != nil {
		resp.RulesError = err.Error()
	}
	return resp
}

func skillPlan(resolver agent.SkillResolver) []SkillPlanView {
	build := resolver.Build(agent.SkillContext{})
	verify := resolver.Verify(agent.SkillContext{}, false)
	repair := resolver.Repair(agent.SkillContext{})
	return []SkillPlanView{
		{Phase: skillrules.PhaseBuild, Skills: orEmpty(build.Names), Source: build.Source},
		{Phase: skillrules.PhaseVerify, Skills: orEmpty(verify.Names), Source: verify.Source},
		{Phase: skillrules.PhaseRepair, Skills: orEmpty(repair.Names), Source: repair.Source},
	}
}

func ruleViews(set skillrules.Set) []SkillRuleView {
	out := make([]SkillRuleView, 0, len(set.Rules))
	for _, r := range set.Rules {
		out = append(out, SkillRuleView{
			Skill:    r.Skill,
			Scope:    skillrules.NormalizeScope(r.Scope),
			Phases:   r.Phases,
			Paths:    r.Paths,
			Keywords: r.Keywords,
		})
	}
	return out
}

func ruleScopes(set skillrules.Set) map[string]string {
	scopes := make(map[string]string, len(set.Rules))
	for _, r := range set.Rules {
		name := strings.TrimSpace(r.Skill)
		if name != "" {
			scopes[name] = skillrules.NormalizeScope(r.Scope)
		}
	}
	return scopes
}

func orEmpty(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

func (s *Server) requiredSkills(repo registry.Repo) (required, requiredVerify []string) {
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		return nil, nil
	}
	return cfg.RequiredSkills, cfg.RequiredSkillsVerify
}

func (s *Server) handleSkillRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	repo, ok := s.findRepo(r.PathValue("repo"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}
	var req SkillRulesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	set := skillrules.Set{Rules: make([]skillrules.Rule, 0, len(req.Rules))}
	for _, v := range req.Rules {
		name := strings.TrimSpace(v.Skill)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "every rule needs a skill"})
			return
		}
		set.Rules = append(set.Rules, skillrules.Rule{
			Skill:    name,
			Scope:    skillrules.NormalizeScope(v.Scope),
			Phases:   v.Phases,
			Paths:    v.Paths,
			Keywords: v.Keywords,
		})
	}
	if err := skillrules.Save(repo.Root, set); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.skillsSnapshot(repo))
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
