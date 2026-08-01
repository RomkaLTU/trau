package webserver

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/folderrepo"
)

// InspectRequest is the body of POST /api/v1/repos/inspect: the absolute path to a
// git repository the wizard wants to size up before registering it.
type InspectRequest struct {
	Path string `json:"path"`
}

// RepoInspection is what the onboarding wizard learns about a candidate repo: its
// git facts, whether a .trau.ini already configures it, which tracker credentials
// exist and at which layer (presence only — never values), and, for a re-run over
// an existing config, the values to pre-fill. A Folder repo has no git facts of
// its own, so Kind says so and Children carries what its Child repos answered
// instead. It is the response of inspect.
type RepoInspection struct {
	Path             string              `json:"path"`
	RepoName         string              `json:"repo_name"`
	Kind             string              `json:"kind"`
	HasTrauIni       bool                `json:"has_trau_ini"`
	DetectedProvider string              `json:"detected_provider,omitempty"`
	Credentials      []InspectCredential `json:"credentials"`
	DefaultBranch    string              `json:"default_branch"`
	Children         []InspectChild      `json:"children,omitempty"`
	Findings         []DetectionFinding  `json:"findings"`
	Prefill          *InspectPrefill     `json:"prefill,omitempty"`
}

// InspectChild is one Child repo of a folder under inspection: its name, the
// default branch it agrees or disagrees on, and whether it can be shipped to.
type InspectChild struct {
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	HasRemote     bool   `json:"has_remote"`
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
	findingFail    = "fail"
)

// childInspectConcurrency bounds the parallel git reads the folder scan spends.
// Both reads are local, so it is process spawn — not git — that a serial scan of
// fifty children would wait on.
const childInspectConcurrency = 8

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

	insp := RepoInspection{
		Path:             root,
		RepoName:         repo.Name,
		Kind:             repoKindRepo,
		HasTrauIni:       hasTrauIni,
		DetectedProvider: provider,
		Credentials:      credentialList(cfg, sources),
	}
	var head []DetectionFinding
	if census := folderrepo.Scan(root); census.IsFolder {
		children := inspectChildren(ctx, census.Children)
		insp.Kind = repoKindFolder
		insp.Children = children
		insp.DefaultBranch = branchOr(majorityBranch(children), cfg.BaseBranch)
		head = folderFindings(children, census.Truncated, insp.DefaultBranch)
	} else {
		origin, branch := inspectGit(ctx, root)
		insp.DefaultBranch = branchOr(branch, cfg.BaseBranch)
		head = []DetectionFinding{
			gitFinding(origin),
			{Label: "default branch", Value: insp.DefaultBranch, State: findingOK},
		}
	}
	insp.Findings = inspectionFindings(cfg, sources, hasTrauIni, explicit, provider, head)

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
	if cfg.HasAzureCredentials() {
		creds = append(creds, InspectCredential{Provider: "azure", Layer: credLayer(sources["AZURE_PAT"])})
	}
	return creds
}

// inspectionFindings completes the report from head, the rows that answer for the
// inspected root's own shape — the git facts for a repo, the child census for a
// folder — with the config rows every kind of repo reports the same way.
func inspectionFindings(cfg config.Config, sources map[string]config.Layer, hasTrauIni, explicit bool, provider string, head []DetectionFinding) []DetectionFinding {
	findings := append(head, trauIniFinding(hasTrauIni, explicit), providerFinding(cfg, explicit, provider))
	return append(findings, credentialFindings(cfg, sources, activeProvider(cfg, explicit, provider))...)
}

func branchOr(branch, fallback string) string {
	if branch == "" {
		return fallback
	}
	return branch
}

func gitFinding(origin string) DetectionFinding {
	if origin == "" {
		return DetectionFinding{
			Label:  "git repository",
			Value:  "yes — no origin remote — trau delivers locally (no push/PR)",
			State:  findingInfo,
			Detail: "Finished work is squash-merged into your default branch instead. Add an origin later to get pushes, PRs and CI.",
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
		credentialFinding("azure", "azure credentials", cfg.HasAzureCredentials(), sources["AZURE_PAT"], active),
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

// activeProviderFrom is the provider a repo would sync as, read from its layered
// config alone. It is the reusable effective-config signal behind both the
// inspection report and the repo health derivation, so a repo reads the same
// either way; an empty result means no effective tracker-provider config, i.e.
// unconfigured.
func activeProviderFrom(cfg config.Config, sources map[string]config.Layer) string {
	explicit := cfg.TrackerProviderExplicit(sources)
	provider := ""
	if explicit {
		provider = strings.ToLower(strings.TrimSpace(cfg.TrackerProvider))
	}
	return activeProvider(cfg, explicit, provider)
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

// inspectChildren reads every Child repo behind a Folder repo, keeping the scan's
// name order.
func inspectChildren(ctx context.Context, found []folderrepo.Child) []InspectChild {
	children := make([]InspectChild, len(found))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(childInspectConcurrency)
	for i, c := range found {
		g.Go(func() error {
			children[i] = inspectChild(gctx, c)
			return nil
		})
	}
	_ = g.Wait()
	return children
}

// inspectChild reads the two facts a run needs from a Child repo: the branch it
// has to agree with the folder's base on, and whether there is an origin to open a
// pull request against. A child whose reads fail carries its zero value rather
// than cancelling the rest.
func inspectChild(ctx context.Context, c folderrepo.Child) InspectChild {
	branch := strings.TrimPrefix(gitOutput(ctx, c.Path, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"), "origin/")
	if branch == "" {
		branch = gitOutput(ctx, c.Path, "rev-parse", "--abbrev-ref", "HEAD")
	}
	return InspectChild{
		Name:          c.Name,
		DefaultBranch: branch,
		HasRemote:     gitOutput(ctx, c.Path, "remote", "get-url", "origin") != "",
	}
}

// majorityBranch is the default branch most Child repos sit on, ties broken
// alphabetically so the same folder always reports the same answer. Reporting the
// majority rather than nothing on disagreement is the point: an empty value only
// makes the wizard fall back to main, which loses the real answer while keeping
// the risk it carries.
func majorityBranch(children []InspectChild) string {
	counts := map[string]int{}
	for _, c := range children {
		if c.DefaultBranch != "" {
			counts[c.DefaultBranch]++
		}
	}
	best := ""
	for _, branch := range slices.Sorted(maps.Keys(counts)) {
		if counts[branch] > counts[best] {
			best = branch
		}
	}
	return best
}

// folderFindings replaces the single-repo git report for a Folder repo, which has
// no origin or HEAD of its own to report. A run applies one base branch to every
// child, so a child sitting on another one is off limits to every run and a change
// landing there aborts it — a failure to fix before onboarding, not a warning.
func folderFindings(children []InspectChild, truncated bool, base string) []DetectionFinding {
	outliers, remoteless := []string{}, []string{}
	for _, c := range children {
		if c.DefaultBranch != "" && c.DefaultBranch != base {
			outliers = append(outliers, c.Name+" on "+c.DefaultBranch)
		}
		if !c.HasRemote {
			remoteless = append(remoteless, c.Name)
		}
	}
	branchState := findingOK
	if len(outliers) > 0 {
		branchState = findingInfo
	}
	findings := []DetectionFinding{
		childCountFinding(len(children), truncated),
		{Label: "default branch", Value: base, State: branchState},
	}
	if len(outliers) > 0 {
		findings = append(findings, DetectionFinding{
			Label:  "branch disagreement",
			Value:  strings.Join(outliers, ", "),
			State:  findingFail,
			Detail: "A run bases every child on " + base + ", so these are off limits to it — and a change landing in one aborts the run. Move them onto " + base + " before onboarding.",
		})
	}
	if len(remoteless) > 0 {
		findings = append(findings, DetectionFinding{
			Label:  "children without a remote",
			Value:  strings.Join(remoteless, ", "),
			State:  findingWarn,
			Detail: "A run cannot open a pull request in these — finished work is squash-merged into their default branch instead.",
		})
	}
	return findings
}

func childCountFinding(count int, truncated bool) DetectionFinding {
	if truncated {
		return DetectionFinding{
			Label:  "child repositories",
			Value:  "more than " + strconv.Itoa(count),
			State:  findingInfo,
			Detail: "The scan stops there; the rest are neither listed nor shipped to.",
		}
	}
	return DetectionFinding{Label: "child repositories", Value: strconv.Itoa(count), State: findingOK}
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
