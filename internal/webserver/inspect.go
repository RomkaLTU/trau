package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
)

// InspectRequest is the body of POST /api/v1/repos/inspect: the absolute path to a
// git repository the wizard wants to size up before registering it.
type InspectRequest struct {
	Path string `json:"path"`
}

// RepoInspection is what the onboarding wizard learns about a candidate repo: its
// git facts, whether a .trau.ini already configures it, which tracker credentials
// exist and at which layer (presence only — never values), and, for a re-run over
// an existing config, the values to pre-fill. It is the response of inspect.
type RepoInspection struct {
	Path             string              `json:"path"`
	RepoName         string              `json:"repo_name"`
	HasTrauIni       bool                `json:"has_trau_ini"`
	DetectedProvider string              `json:"detected_provider,omitempty"`
	Credentials      []InspectCredential `json:"credentials"`
	DefaultBranch    string              `json:"default_branch"`
	Findings         []DetectionFinding  `json:"findings"`
	Prefill          *InspectPrefill     `json:"prefill,omitempty"`
}

// InspectCredential records that a provider's credentials exist and the config
// layer they live in — presence and layer only, never the secret value.
type InspectCredential struct {
	Provider string `json:"provider"`
	Layer    string `json:"layer"`
}

// DetectionFinding is one row of the detection report: a labelled fact whose state
// drives its callout color, with an optional detail line.
type DetectionFinding struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// InspectPrefill carries the wizard's re-run defaults, read from an existing
// config so a second pass opens pre-filled instead of blank.
type InspectPrefill struct {
	Provider   string `json:"provider"`
	Team       string `json:"team"`
	ReadyLabel string `json:"ready_label"`
	EpicFlow   bool   `json:"epic_flow"`
}

const (
	findingOK      = "ok"
	findingWarn    = "warn"
	findingMissing = "missing"
	findingInfo    = "info"
)

// handleReposInspect reports what trau finds at a repo path so the wizard can show
// an honest detection report before anything is written. Inspecting arbitrary host
// paths is registration-grade capability, so it follows the same exposure gate:
// refused on a non-loopback bind unless SERVE_ALLOW_REGISTER is set.
func (s *Server) handleReposInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "inspecting a repo") {
		return
	}
	var req InspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	root, err := validateRepoPath(req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.inspectRepo(r.Context(), root))
}

// inspectRepo resolves the repo's layered config and git facts into the detection
// report. Config is read layer-aware so findings can name where a credential lives
// and, above all, surface the melga trap: credentials present but TRACKER_PROVIDER
// unset, where sync would silently guess wrong. No secret value is ever read out.
func (s *Server) inspectRepo(ctx context.Context, root string) RepoInspection {
	repo := workspaceRepo(root)
	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, sources, _ := config.LoadLayeredWithSources(projectPath, userPath, "", "")

	_, statErr := os.Stat(projectPath)
	hasTrauIni := statErr == nil
	explicit := cfg.TrackerProviderExplicit(sources)
	provider := ""
	if explicit {
		provider = strings.ToLower(strings.TrimSpace(cfg.TrackerProvider))
	}

	origin, branch := inspectGit(ctx, root)
	if branch == "" {
		branch = cfg.BaseBranch
	}

	insp := RepoInspection{
		Path:             root,
		RepoName:         repo.Name,
		HasTrauIni:       hasTrauIni,
		DetectedProvider: provider,
		Credentials:      credentialList(cfg, sources),
		DefaultBranch:    branch,
		Findings:         inspectionFindings(cfg, sources, hasTrauIni, explicit, provider, origin, branch),
	}
	if hasTrauIni && explicit {
		insp.Prefill = &InspectPrefill{
			Provider:   provider,
			Team:       prefillTeam(cfg),
			ReadyLabel: cfg.ReadyLabel,
			EpicFlow:   cfg.EpicFlow,
		}
	}
	return insp
}

func prefillTeam(cfg config.Config) string {
	if team := strings.TrimSpace(cfg.LinearTeam); team != "" {
		return team
	}
	return strings.TrimSpace(cfg.Project)
}

func credentialList(cfg config.Config, sources map[string]config.Layer) []InspectCredential {
	creds := []InspectCredential{}
	if strings.TrimSpace(cfg.LinearAPIKey) != "" {
		creds = append(creds, InspectCredential{Provider: "linear", Layer: credLayer(sources["LINEAR_API_KEY"])})
	}
	if cfg.HasJiraCredentials() {
		creds = append(creds, InspectCredential{Provider: "jira", Layer: credLayer(sources["JIRA_API_TOKEN"])})
	}
	return creds
}

func inspectionFindings(cfg config.Config, sources map[string]config.Layer, hasTrauIni, explicit bool, provider, origin, branch string) []DetectionFinding {
	findings := []DetectionFinding{
		gitFinding(origin),
		{Label: "default branch", Value: branch, State: findingOK},
		trauIniFinding(hasTrauIni, explicit),
		providerFinding(cfg, explicit, provider),
	}
	return append(findings, credentialFindings(cfg, sources, activeProvider(cfg, explicit, provider))...)
}

func gitFinding(origin string) DetectionFinding {
	if origin == "" {
		return DetectionFinding{
			Label:  "git repository",
			Value:  "yes — no origin remote",
			State:  findingWarn,
			Detail: "trau pushes branches and opens PRs against origin; add one before running.",
		}
	}
	return DetectionFinding{Label: "git repository", Value: "yes — origin " + origin, State: findingOK}
}

func trauIniFinding(hasTrauIni, explicit bool) DetectionFinding {
	switch {
	case !hasTrauIni:
		return DetectionFinding{
			Label:  ".trau.ini",
			Value:  "not found",
			State:  findingMissing,
			Detail: "The wizard writes a new project config and gitignores it.",
		}
	case !explicit:
		return DetectionFinding{
			Label:  ".trau.ini",
			Value:  "found — partial",
			State:  findingWarn,
			Detail: "Config exists but is missing required keys.",
		}
	default:
		return DetectionFinding{
			Label:  ".trau.ini",
			Value:  "found — complete",
			State:  findingOK,
			Detail: "Re-running the wizard pre-fills from this config.",
		}
	}
}

// providerFinding surfaces the melga trap: a repo with tracker credentials but no
// explicit TRACKER_PROVIDER, where sync falls back to Linear and fails silently.
func providerFinding(cfg config.Config, explicit bool, provider string) DetectionFinding {
	switch {
	case explicit:
		return DetectionFinding{Label: "tracker provider", Value: provider, State: findingOK}
	case cfg.HasJiraCredentials():
		return DetectionFinding{
			Label:  "tracker provider",
			Value:  "NOT SET — sync would guess wrong",
			State:  findingWarn,
			Detail: "Jira credentials are present but TRACKER_PROVIDER is unset. Without it, sync falls back to Linear and fails.",
		}
	default:
		return DetectionFinding{
			Label:  "tracker provider",
			Value:  "not set",
			State:  findingMissing,
			Detail: "Pick one in the next step — trau never guesses.",
		}
	}
}

func credentialFindings(cfg config.Config, sources map[string]config.Layer, active string) []DetectionFinding {
	return []DetectionFinding{
		credentialFinding("linear", "linear credentials", strings.TrimSpace(cfg.LinearAPIKey) != "", sources["LINEAR_API_KEY"], active),
		credentialFinding("jira", "jira credentials", cfg.HasJiraCredentials(), sources["JIRA_API_TOKEN"], active),
	}
}

func credentialFinding(prov, label string, present bool, layer config.Layer, active string) DetectionFinding {
	if !present {
		state := findingInfo
		if prov == active || active == "" {
			state = findingMissing
		}
		return DetectionFinding{Label: label, Value: "none", State: state}
	}
	finding := DetectionFinding{Label: label, Value: credentialLocation(layer), State: findingInfo}
	switch {
	case prov == active:
		finding.State = findingOK
	case layer == config.LayerUser:
		finding.Detail = "User-layer key is shared by all projects on this machine."
	}
	return finding
}

// activeProvider is the provider trau would actually sync as, used to color the
// credential findings. An explicit TRACKER_PROVIDER wins; otherwise present Jira
// credentials imply Jira (the reason the melga case reads its creds as OK), and a
// bare repo has no confirmed provider yet.
func activeProvider(cfg config.Config, explicit bool, provider string) string {
	switch {
	case explicit:
		return provider
	case cfg.HasJiraCredentials():
		return "jira"
	default:
		return ""
	}
}

func credLayer(layer config.Layer) string {
	switch layer {
	case config.LayerProject, config.LayerLocal:
		return "project"
	case config.LayerUser, config.LayerEnv:
		return "user"
	default:
		return "none"
	}
}

func credentialLocation(layer config.Layer) string {
	switch layer {
	case config.LayerProject, config.LayerLocal:
		return "found in project config (.trau.ini)"
	case config.LayerUser:
		return "found in user config (~/.trau.ini)"
	case config.LayerEnv:
		return "found in the hub environment"
	default:
		return "found"
	}
}

// inspectGit reads the repo's origin remote and default branch best-effort: a repo
// without git set up (or without an origin) simply yields empty strings, which the
// findings render as their own states rather than failing the inspection.
func inspectGit(ctx context.Context, root string) (origin, branch string) {
	origin = gitOutput(ctx, root, "remote", "get-url", "origin")
	branch = strings.TrimPrefix(gitOutput(ctx, root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"), "origin/")
	if branch == "" {
		branch = gitOutput(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	}
	return origin, branch
}

func gitOutput(ctx context.Context, root string, args ...string) string {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
