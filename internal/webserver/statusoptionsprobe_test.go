package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker"
)

// probeServer wires a hub with no registered repo — the onboarding case, where the
// credentials exist only in the wizard's form — and records the config the
// provider arm was reached with.
func probeServer(t *testing.T, seen *config.Config) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.statusOptions = func(_ context.Context, _ string, cfg config.Config) (tracker.StatusOptions, error) {
		if seen != nil {
			*seen = cfg
		}
		return tracker.StatusOptions{
			Columns: []tracker.BoardColumnSuggestion{{Name: "Icebox", SuggestedGroup: "backlog"}},
			Pins:    []tracker.WorkflowOption{{Name: "Icebox", Category: "backlog"}},
		}, nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postStatusOptions(t *testing.T, ts *httptest.Server, provider, body string) (*http.Response, StatusOptionsResponse) {
	t.Helper()
	res, err := http.Post(
		ts.URL+APIPrefix+"/trackers/"+provider+"/status-options",
		"application/json", bytes.NewBufferString(body),
	)
	if err != nil {
		t.Fatalf("POST status-options: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out StatusOptionsResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode status-options: %v", err)
		}
	}
	return res, out
}

// The wizard's own credentials and its chosen team reach the provider arm and come
// back in the same shape the repo-scoped read answers with, so one editor renders
// either source.
func TestStatusOptionsProbeReadsInFormCredentials(t *testing.T) {
	var seen config.Config
	ts := probeServer(t, &seen)

	res, out := postStatusOptions(t, ts, "linear",
		`{"api_key":"lin_typed","binding":"COD"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if seen.LinearAPIKey != "lin_typed" {
		t.Errorf("read with API key %q, want the one the form typed", seen.LinearAPIKey)
	}
	if seen.LinearTeam != "COD" {
		t.Errorf("read with team %q, want the chosen COD", seen.LinearTeam)
	}
	if out.Provider != "linear" {
		t.Errorf("provider = %q, want linear", out.Provider)
	}
	if len(out.Grouping) != 1 || out.Grouping[0] != (StatusColumn{Name: "Icebox", SuggestedGroup: "backlog"}) {
		t.Errorf("grouping = %+v, want the arm's own columns", out.Grouping)
	}
	if len(out.PinOptions) != 1 || out.PinOptions[0] != (StatusPinOption{Name: "Icebox", Category: "backlog"}) {
		t.Errorf("pinOptions = %+v, want the arm's own pins", out.PinOptions)
	}
}

// Jira and Azure DevOps bind to a project, and the same field carries it — the
// binding is what TrackerKey resolves whichever provider is asked.
func TestStatusOptionsProbeCarriesEachProvidersBinding(t *testing.T) {
	for _, tc := range []struct {
		provider string
		body     string
		check    func(cfg config.Config) string
	}{
		{
			provider: "jira",
			body:     `{"base_url":"https://acme.atlassian.net","email":"me@acme.com","api_token":"tok","binding":"PROJ"}`,
			check: func(cfg config.Config) string {
				if cfg.JiraBaseURL != "https://acme.atlassian.net" || cfg.JiraEmail != "me@acme.com" || cfg.JiraAPIToken != "tok" {
					return "jira credentials did not reach the arm"
				}
				return ""
			},
		},
		{
			provider: "azure",
			body:     `{"base_url":"https://dev.azure.com/acme","api_token":"pat","binding":"Platform"}`,
			check: func(cfg config.Config) string {
				if cfg.AzureOrgURL != "https://dev.azure.com/acme" || cfg.AzurePAT != "pat" {
					return "azure credentials did not reach the arm"
				}
				return ""
			},
		},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var seen config.Config
			ts := probeServer(t, &seen)
			res, _ := postStatusOptions(t, ts, tc.provider, tc.body)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if msg := tc.check(seen); msg != "" {
				t.Error(msg)
			}
			if seen.TrackerKey() == "" {
				t.Error("binding did not reach the arm as the tracker key")
			}
		})
	}
}

// A blank secret means "use what the named repo already stores", the write-only
// semantics the connection test follows, so a re-run reads options without
// re-typing the key.
func TestStatusOptionsProbeFallsBackToTheStoredCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	writeRepoINI(t, filepath.Dir(filepath.Dir(runsDir)),
		"TRACKER_PROVIDER=linear\nLINEAR_TEAM=OLD\nLINEAR_API_KEY=lin_stored\n")

	var seen config.Config
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	s.statusOptions = func(_ context.Context, _ string, cfg config.Config) (tracker.StatusOptions, error) {
		seen = cfg
		return tracker.StatusOptions{}, nil
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, _ := postStatusOptions(t, ts, "linear", `{"repo":"acme","binding":"COD"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if seen.LinearAPIKey != "lin_stored" {
		t.Errorf("read with API key %q, want the repo's stored key", seen.LinearAPIKey)
	}
	if seen.LinearTeam != "COD" {
		t.Errorf("read with team %q, want the binding the request chose over the stored one", seen.LinearTeam)
	}
}

// The options are read under one container, so a request without one is refused
// rather than answered against whatever the credentials happen to reach first.
func TestStatusOptionsProbeRequiresABinding(t *testing.T) {
	for _, tc := range []struct{ provider, noun string }{
		{"linear", "team"},
		{"jira", "project"},
		{"azure", "project"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			ts := probeServer(t, nil)
			res, _ := postStatusOptions(t, ts, tc.provider, `{"api_key":"k","binding":"  "}`)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
			body := readProbeError(t, ts, tc.provider, `{"api_key":"k"}`)
			if !strings.Contains(body, tc.noun) {
				t.Errorf("error = %q, want the missing %s named", body, tc.noun)
			}
		})
	}
}

// A credential the request never carried is named the way the connection test
// names it, inside a 200 the editor can degrade from — a missing key is a gap to
// fill, not a broken endpoint.
func TestStatusOptionsProbeNamesMissingCredentials(t *testing.T) {
	ts := probeServer(t, nil)

	res, out := postStatusOptions(t, ts, "jira", `{"binding":"PROJ"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	for _, want := range []string{"Jira site URL", "account email", "API token"} {
		if !strings.Contains(out.Error, want) {
			t.Errorf("error = %q, want %q named", out.Error, want)
		}
	}
	if !strings.Contains(out.Hint, "API token") {
		t.Errorf("hint = %q, want the Jira remediation", out.Hint)
	}
	if out.Grouping == nil || out.PinOptions == nil {
		t.Error("grouping/pinOptions are null, want empty lists so the editor can render its fallback")
	}
}

// Only the trackers whose metadata carries both halves of a mapping answer here;
// every other provider is a 404, which is the same answer the repo-scoped read
// gives and the one the wizard reads as "no section for this provider".
func TestStatusOptionsProbeGatesUnsupportedProviders(t *testing.T) {
	for _, provider := range []string{"github", "internal", "nonesuch"} {
		t.Run(provider, func(t *testing.T) {
			ts := probeServer(t, nil)
			res, _ := postStatusOptions(t, ts, provider, `{"binding":"COD"}`)
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", res.StatusCode)
			}
		})
	}
}

func TestStatusOptionsProbeRejectsABadBodyAndTheWrongMethod(t *testing.T) {
	ts := probeServer(t, nil)

	res, _ := postStatusOptions(t, ts, "linear", `{`)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unparsable body", res.StatusCode)
	}

	get, err := http.Get(ts.URL + APIPrefix + "/trackers/linear/status-options")
	if err != nil {
		t.Fatalf("GET status-options: %v", err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for a GET", get.StatusCode)
	}
}

func readProbeError(t *testing.T, ts *httptest.Server, provider, body string) string {
	t.Helper()
	res, err := http.Post(
		ts.URL+APIPrefix+"/trackers/"+provider+"/status-options",
		"application/json", bytes.NewBufferString(body),
	)
	if err != nil {
		t.Fatalf("POST status-options: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]string
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return out["error"]
}
