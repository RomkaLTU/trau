package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/skillrules"
)

const (
	skillsRegistryURL = "https://skills.sh/api/search"
	skillsPageBase    = "https://skills.sh/"
	skillsSearchTTL   = 60 * time.Second

	// skillCoverageDays is the activation window the panel reads run evidence
	// over: a skill no call in it loaded reads as dead, not merely quiet.
	skillCoverageDays = 30

	// skillCoveragePhases caps the plan-versus-loaded list at a page's worth of
	// the most recent phase attempts.
	skillCoveragePhases = 12
)

var (
	defaultInstallSkill = agent.InstallSkillPackage
	defaultRemoveSkill  = agent.RemoveSkill
	skillsHTTPClient    = &http.Client{Timeout: 10 * time.Second}
	skillProviders      = agent.DefaultRegistry()
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
	Loads             int      `json:"loads"`
	LastLoaded        string   `json:"last_loaded,omitempty"`
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
// floor rather than any particular slice's set. Fallback marks a phase that
// nothing scoped to it, left to the chain's backstop.
type SkillPlanView struct {
	Phase    string            `json:"phase"`
	Skills   []string          `json:"skills"`
	Source   string            `json:"source"`
	Origins  map[string]string `json:"origins,omitempty"`
	Fallback bool              `json:"fallback,omitempty"`
}

// SkillPhaseView pairs one recent phase attempt's planned set with the skills
// its agent reported loading. Unknown marks an attempt whose provider does not
// report skill usage, or that no recorded call could be matched to: its loaded
// side is unrecoverable rather than empty.
type SkillPhaseView struct {
	Ticket   string   `json:"ticket"`
	Phase    string   `json:"phase"`
	TS       string   `json:"ts"`
	Provider string   `json:"provider,omitempty"`
	Planned  []string `json:"planned"`
	Loaded   []string `json:"loaded"`
	Unknown  bool     `json:"unknown,omitempty"`
}

// SkillCoverageView is the repo's activation evidence over the last Days days.
// HasData is false when nothing in the window could report skill usage — no runs
// at all, or only providers that never report it — and every skill's load count
// then says nothing about whether it is dead. Silent names the providers that ran
// without reporting.
type SkillCoverageView struct {
	Days    int              `json:"days"`
	HasData bool             `json:"has_data"`
	Silent  []string         `json:"silent_providers,omitempty"`
	Phases  []SkillPhaseView `json:"phases"`
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
	Coverage    SkillCoverageView         `json:"coverage"`
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

	coverage, loads := s.skillCoverage(repo.Root)

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
			Loads:             loads[meta.Name].count,
			LastLoaded:        loads[meta.Name].lastTS,
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
		Coverage:    coverage,
		Unknown:     resolver.UnknownRuleSkills(),
	}
	if err := resolver.RulesError(); err != nil {
		resp.RulesError = err.Error()
	}
	return resp
}

func skillPlan(resolver agent.SkillResolver) []SkillPlanView {
	return []SkillPlanView{
		planView(skillrules.PhaseBuild, resolver.Build(agent.SkillContext{})),
		planView(skillrules.PhaseVerify, resolver.Verify(agent.SkillContext{}, false)),
		planView(skillrules.PhaseRepair, resolver.Repair(agent.SkillContext{})),
	}
}

func planView(phase string, set agent.SkillSet) SkillPlanView {
	return SkillPlanView{
		Phase:    phase,
		Skills:   orEmpty(set.Names),
		Source:   set.Source,
		Origins:  set.Origins,
		Fallback: set.Source == agent.SkillsSourceRecommended || set.Source == agent.SkillsSourceInstalled,
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

// skillLoad is one skill's evidence inside the coverage window.
type skillLoad struct {
	count  int
	lastTS string
}

// skillCoverage reads the repo's recent activation evidence: how often each
// skill was loaded and when it last was, plus the recent phase attempts with
// their planned set beside the loaded one. A provider that never reports skill
// usage contributes no evidence, so a repo running only those reads as "no data"
// rather than as one whose every skill went unused.
func (s *Server) skillCoverage(root string) (SkillCoverageView, map[string]skillLoad) {
	view := SkillCoverageView{Days: skillCoverageDays, Phases: []SkillPhaseView{}}
	loads := map[string]skillLoad{}

	cutoff := time.Now().AddDate(0, 0, -skillCoverageDays)
	calls, err := s.stores.Tokens().SkillCalls(root, cutoff.Format(time.DateOnly))
	if err != nil {
		return view, loads
	}
	for _, c := range calls {
		if !reportsSkills(c.Provider) {
			view.Silent = appendSilentProvider(view.Silent, c.Provider)
			continue
		}
		view.HasData = true
		for _, name := range c.Skills {
			l := loads[name]
			l.count++
			if c.TS > l.lastTS {
				l.lastTS = c.TS
			}
			loads[name] = l
		}
	}
	view.Phases = s.skillPhases(root, cutoff, calls)
	return view, loads
}

// skillPhases pairs each recent planned set with the call that ran under it —
// the first call for the same ticket whose phase label starts with the planned
// phase, so a verify-retry2 call pairs with the verify plan — newest first.
func (s *Server) skillPhases(root string, since time.Time, calls []hubstore.SkillCall) []SkillPhaseView {
	evs, err := s.stores.Events().Query(root, hubstore.EventFilter{
		Kind:  event.KindSkillsPlanned,
		Since: since,
		Limit: skillCoveragePhases,
	})
	if err != nil {
		return []SkillPhaseView{}
	}
	out := make([]SkillPhaseView, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		ticket, planned := plannedSkills(ev.Fields)
		v := SkillPhaseView{
			Ticket:  ticket,
			Phase:   ev.Phase,
			TS:      ev.TS,
			Planned: planned,
			Loaded:  []string{},
			Unknown: true,
		}
		if c, ok := callUnderPlan(calls, ticket, ev.Phase, ev.TS); ok {
			v.Provider = c.Provider
			v.Loaded = orEmpty(c.Skills)
			v.Unknown = !reportsSkills(c.Provider)
		}
		out = append(out, v)
	}
	return out
}

func callUnderPlan(calls []hubstore.SkillCall, ticket, phase, ts string) (hubstore.SkillCall, bool) {
	planned := wallClock(ts)
	for _, c := range calls {
		if c.Ticket == ticket && wallClock(c.TS) >= planned && strings.HasPrefix(c.Phase, phase) {
			return c, true
		}
	}
	return hubstore.SkillCall{}, false
}

// wallClock trims a timestamp to the local wall clock both sides share: events
// carry an RFC3339 offset and token calls do not, so only the prefix compares.
func wallClock(ts string) string {
	if len(ts) > len("2006-01-02T15:04:05") {
		return ts[:len("2006-01-02T15:04:05")]
	}
	return ts
}

func plannedSkills(fields string) (ticket string, skills []string) {
	var f struct {
		Ticket string   `json:"ticket"`
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal([]byte(fields), &f); err != nil {
		return "", []string{}
	}
	return f.Ticket, orEmpty(f.Skills)
}

func reportsSkills(provider string) bool {
	spec, ok := skillProviders.Lookup(provider)
	return ok && spec.ReportsSkills
}

func appendSilentProvider(dst []string, provider string) []string {
	if provider == "" || slices.Contains(dst, provider) {
		return dst
	}
	return append(dst, provider)
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
