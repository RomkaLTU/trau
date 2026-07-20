package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

// TestConnectionRequest is the body of POST /api/v1/trackers/{provider}/test-connection:
// the credentials the onboarding wizard has typed for the chosen provider — linear an
// API key, jira a base URL, email and API token — plus an optional repo whose stored
// config fills any field left blank. A blank secret means "use the value already stored
// for that repo" (write-only semantics, ADR 0011), so a re-run can test without
// re-typing the key.
type TestConnectionRequest struct {
	Repo     string `json:"repo,omitempty"`
	APIKey   string `json:"api_key,omitempty"`   // linear API key
	BaseURL  string `json:"base_url,omitempty"`  // jira site base URL
	Email    string `json:"email,omitempty"`     // jira account email
	APIToken string `json:"api_token,omitempty"` // jira API token
}

// TestConnectionResponse is the outcome of the probe. On success it carries the
// visible-issue count and the accessible teams (Linear) or projects (Jira) that feed
// the wizard's picker. On failure it carries the provider error verbatim plus a hint
// when the failure shape is recognizable. No secret value ever appears in either shape.
type TestConnectionResponse struct {
	OK            bool           `json:"ok"`
	IssuesVisible int            `json:"issues_visible,omitempty"`
	Teams         []tracker.Team `json:"teams,omitempty"`
	Error         string         `json:"error,omitempty"`
	Hint          string         `json:"hint,omitempty"`
}

// trackerProbe is the throwaway credential reader the connection test drives: a cheap
// authenticated issue count and the selectable containers, nothing persisted. Linear
// and Jira each satisfy it over their direct API; the seam lets tests point it at a
// fake server.
type trackerProbe interface {
	CountIssues(ctx context.Context) (int, error)
	ListTeams(ctx context.Context) ([]tracker.Team, error)
}

// handleTrackerTestConnection gates the wizard's tracker step: it builds a throwaway
// reader from the submitted (or stored) credentials, performs a cheap authenticated
// read, and reports the accessible containers and visible-issue count — or the error
// and a hint. It receives raw secrets over the wire, so it follows the registration
// exposure gate and never logs or echoes a secret value.
func (s *Server) handleTrackerTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.denyRegistrationIfExposed(w, "testing a tracker connection") {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	if provider == "internal" {
		writeJSON(w, http.StatusOK, TestConnectionResponse{OK: true})
		return
	}
	var req TestConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if provider != "linear" && provider != "jira" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported tracker provider %q", provider)})
		return
	}
	writeJSON(w, http.StatusOK, s.testTrackerConnection(r.Context(), provider, s.connConfig(provider, req)))
}

// connConfig overlays the request credentials onto the repo's stored config: any field
// the wizard left blank falls back to the stored value for the named repo, so a re-run
// tests against the existing key without re-typing it. An absent or unknown repo leaves
// the stored side empty, so the request must then carry the full credential set.
func (s *Server) connConfig(provider string, req TestConnectionRequest) config.Config {
	var stored config.Config
	if repo, ok := s.findRepo(strings.TrimSpace(req.Repo)); ok {
		projectPath, userPath := s.repoConfigPaths(repo)
		stored, _, _ = config.LoadLayeredWithSources(projectPath, userPath, "", "")
	}
	cfg := config.Config{}
	switch provider {
	case "linear":
		cfg.LinearAPIKey = firstNonEmpty(req.APIKey, stored.LinearAPIKey)
	case "jira":
		cfg.JiraBaseURL = firstNonEmpty(req.BaseURL, stored.JiraBaseURL)
		cfg.JiraEmail = firstNonEmpty(req.Email, stored.JiraEmail)
		cfg.JiraAPIToken = firstNonEmpty(req.APIToken, stored.JiraAPIToken)
	}
	return cfg
}

// testTrackerConnection builds the throwaway reader and runs the probe under a bounded
// timeout, shaping the outcome into the response the wizard renders.
func (s *Server) testTrackerConnection(ctx context.Context, provider string, cfg config.Config) TestConnectionResponse {
	if missing := missingCredentials(provider, cfg); len(missing) > 0 {
		return TestConnectionResponse{
			Error: "missing " + strings.Join(missing, ", "),
			Hint:  enterCredentialsHint(provider),
		}
	}
	probe, err := s.newProbe(provider, cfg)
	if err != nil {
		return TestConnectionResponse{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	count, err := probe.CountIssues(ctx)
	if err != nil {
		return connFailure(provider, err)
	}
	teams, err := probe.ListTeams(ctx)
	if err != nil {
		return connFailure(provider, err)
	}
	if len(teams) == 0 {
		noun := containerNoun(provider)
		return TestConnectionResponse{
			Error: "no " + noun + " are visible to these credentials",
			Hint:  "The credentials authenticated but grant access to no " + noun + "; check the account has been added to at least one.",
		}
	}
	return TestConnectionResponse{OK: true, IssuesVisible: count, Teams: teams}
}

// connFailure renders a probe error as a failure response, attaching a hint when the
// failure shape is one the wizard can act on: a rejected token or an unreachable
// base URL.
func connFailure(provider string, err error) TestConnectionResponse {
	return TestConnectionResponse{Error: err.Error(), Hint: connHint(provider, err)}
}

// missingCredentials lists the credential fields the merged config still lacks
// for provider, in the order the wizard shows them. A non-empty result means the
// probe could only fail with the provider's opaque "not enabled" sentinel, so the
// handler names the gap directly instead.
func missingCredentials(provider string, cfg config.Config) []string {
	blank := func(v string) bool { return strings.TrimSpace(v) == "" }
	var missing []string
	switch provider {
	case "linear":
		if blank(cfg.LinearAPIKey) {
			missing = append(missing, "Linear API key")
		}
	case "jira":
		if blank(cfg.JiraBaseURL) {
			missing = append(missing, "Jira site URL")
		}
		if blank(cfg.JiraEmail) {
			missing = append(missing, "account email")
		}
		if blank(cfg.JiraAPIToken) {
			missing = append(missing, "API token")
		}
	}
	return missing
}

func enterCredentialsHint(provider string) string {
	if provider == "jira" {
		return "Enter the Jira site URL, account email, and API token."
	}
	return "Enter a Linear API key."
}

// connHint maps a recognizable probe failure onto an actionable, secret-free hint.
func connHint(provider string, err error) string {
	switch provider {
	case "jira":
		if msg := jiraapi.AuthErrorMessage(err); msg != "" {
			return msg
		}
		if isUnreachable(err) {
			return "Could not reach the Jira site — check the base URL is a reachable https://<site>.atlassian.net."
		}
	case "linear":
		if errors.Is(err, linearapi.ErrUnauthorized) {
			return "Linear rejected the API key — check it is a valid personal API key."
		}
	}
	return ""
}

// isUnreachable reports whether err is a transport-level failure to reach the host — a
// mistyped or unresolvable base URL — rather than an authenticated rejection.
func isUnreachable(err error) bool {
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func containerNoun(provider string) string {
	if provider == "jira" {
		return "projects"
	}
	return "teams"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// defaultProbe builds the direct-API reader for a provider from cfg. Linear reads the
// GraphQL API with the key; Jira reads the REST API as the entered Basic-auth identity,
// its base URL doubling as the endpoint. Nothing is persisted.
func defaultProbe(provider string, cfg config.Config) (trackerProbe, error) {
	switch provider {
	case "linear":
		return linearProbe{client: linearapi.New(cfg.LinearAPIKey)}, nil
	case "jira":
		return jiraProbe{client: jiraapi.New(cfg.JiraBaseURL, cfg.JiraEmail, cfg.JiraAPIToken)}, nil
	default:
		return nil, fmt.Errorf("unsupported tracker provider %q", provider)
	}
}

type linearProbe struct{ client *linearapi.Client }

func (p linearProbe) CountIssues(ctx context.Context) (int, error) { return p.client.CountIssues(ctx) }

func (p linearProbe) ListTeams(ctx context.Context) ([]tracker.Team, error) {
	teams, err := p.client.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Team, 0, len(teams))
	for _, t := range teams {
		out = append(out, tracker.Team{Key: t.Key, Name: t.Name})
	}
	return out, nil
}

type jiraProbe struct{ client *jiraapi.Client }

func (p jiraProbe) CountIssues(ctx context.Context) (int, error) { return p.client.CountIssues(ctx) }

func (p jiraProbe) ListTeams(ctx context.Context) ([]tracker.Team, error) {
	projects, err := p.client.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Team, 0, len(projects))
	for _, pr := range projects {
		if pr.Key == "" {
			continue
		}
		out = append(out, tracker.Team{Key: pr.Key, Name: pr.Name})
	}
	return out, nil
}
