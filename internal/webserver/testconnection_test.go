package webserver

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/tracker/jiraapi"
	"github.com/RomkaLTU/trau/internal/tracker/linearapi"
)

func connServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	return s
}

func startConn(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postConn(t *testing.T, ts *httptest.Server, provider string, req TestConnectionRequest) (*http.Response, TestConnectionResponse) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/trackers/"+provider+"/test-connection", req)
	var out TestConnectionResponse
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return res, out
}

// linearFake serves the count and teams GraphQL queries from a single fake endpoint.
func linearFake(t *testing.T, count, teamsJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "CountIssues"):
			_, _ = io.WriteString(w, count)
		case strings.Contains(string(body), "query Teams"):
			_, _ = io.WriteString(w, teamsJSON)
		default:
			t.Errorf("unexpected linear query: %s", body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func linearProbeSeam(fakeURL string) func(string, config.Config) (trackerProbe, error) {
	return func(provider string, cfg config.Config) (trackerProbe, error) {
		c := linearapi.New(cfg.LinearAPIKey)
		c.Endpoint = fakeURL
		return linearProbe{client: c}, nil
	}
}

func TestTrackerTestConnectionLinearHappyPath(t *testing.T) {
	fake := linearFake(t,
		`{"data":{"issues":{"nodes":[{"id":"1"},{"id":"2"}]}}}`,
		`{"data":{"teams":{"nodes":[{"id":"t","key":"COD","name":"Codes"}]}}}`,
	)
	s := connServer(t)
	s.newProbe = linearProbeSeam(fake.URL)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "linear", TestConnectionRequest{APIKey: "lin_key"})
	if !out.OK {
		t.Fatalf("ok = false, resp = %+v", out)
	}
	if out.IssuesVisible != 2 {
		t.Errorf("issues_visible = %d, want 2", out.IssuesVisible)
	}
	if len(out.Teams) != 1 || out.Teams[0].Key != "COD" || out.Teams[0].Name != "Codes" {
		t.Errorf("teams = %+v, want [{COD Codes}]", out.Teams)
	}
}

func TestTrackerTestConnectionLinearBadKey(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fake.Close()

	s := connServer(t)
	s.newProbe = linearProbeSeam(fake.URL)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "linear", TestConnectionRequest{APIKey: "bad"})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	if out.Error == "" {
		t.Errorf("error empty, want the provider error verbatim")
	}
	if !strings.Contains(strings.ToLower(out.Hint), "api key") {
		t.Errorf("hint = %q, want a bad-key hint", out.Hint)
	}
}

func TestTrackerTestConnectionJiraHappyPath(t *testing.T) {
	var gotAuth string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/search/approximate-count":
			_, _ = io.WriteString(w, `{"count":12}`)
		case "/rest/api/3/project/search":
			_, _ = io.WriteString(w, `{"values":[{"key":"ENG","name":"Engineering"}],"isLast":true,"total":1}`)
		default:
			t.Errorf("unexpected jira path %s", r.URL.Path)
		}
	}))
	defer fake.Close()

	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{BaseURL: fake.URL, Email: "me@acme.com", APIToken: "tok"})
	if !out.OK {
		t.Fatalf("ok = false, resp = %+v", out)
	}
	if out.IssuesVisible != 12 {
		t.Errorf("issues_visible = %d, want 12", out.IssuesVisible)
	}
	if len(out.Teams) != 1 || out.Teams[0].Key != "ENG" || out.Teams[0].Name != "Engineering" {
		t.Errorf("teams = %+v, want [{ENG Engineering}]", out.Teams)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@acme.com:tok"))
	if gotAuth != wantAuth {
		t.Errorf("auth = %q, want %q", gotAuth, wantAuth)
	}
}

func TestTrackerTestConnectionJiraBadToken(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fake.Close()

	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{BaseURL: fake.URL, Email: "e@x.com", APIToken: "bad-secret-token"})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	if !strings.Contains(out.Hint, jiraapi.TokenHelpURL) {
		t.Errorf("hint = %q, want it to point at the token help URL", out.Hint)
	}
}

func TestTrackerTestConnectionJiraUnreachableBaseURL(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closed := fake.URL
	fake.Close() // the port now refuses connections

	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{BaseURL: closed, Email: "e@x.com", APIToken: "tok"})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	if !strings.Contains(strings.ToLower(out.Hint), "reach") {
		t.Errorf("hint = %q, want an unreachable-base-URL hint", out.Hint)
	}
}

// TestTrackerTestConnectionJiraMissingBaseURL reproduces the onboarding payload a
// customer hit: email and token entered, site URL blank, repo not yet configured.
// The response must name the missing field, not the opaque "not enabled" sentinel.
func TestTrackerTestConnectionJiraMissingBaseURL(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{Repo: "melga", Email: "e@x.com", APIToken: "tok"})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	if out.Error != "missing Jira site URL" {
		t.Errorf("error = %q, want %q", out.Error, "missing Jira site URL")
	}
	if !strings.Contains(out.Hint, "site URL") {
		t.Errorf("hint = %q, want the enter-credentials hint", out.Hint)
	}
}

func TestTrackerTestConnectionJiraMissingAllFields(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	want := "missing Jira site URL, account email, API token"
	if out.Error != want {
		t.Errorf("error = %q, want %q", out.Error, want)
	}
}

func TestTrackerTestConnectionLinearMissingKey(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "linear", TestConnectionRequest{})
	if out.OK {
		t.Fatalf("ok = true, want false")
	}
	if out.Error != "missing Linear API key" {
		t.Errorf("error = %q, want %q", out.Error, "missing Linear API key")
	}
	if out.Hint == "" {
		t.Errorf("hint empty, want an enter-key hint")
	}
}

func TestTrackerTestConnectionEmptyProjects(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/search/approximate-count":
			_, _ = io.WriteString(w, `{"count":5}`)
		case "/rest/api/3/project/search":
			_, _ = io.WriteString(w, `{"values":[],"isLast":true,"total":0}`)
		}
	}))
	defer fake.Close()

	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{BaseURL: fake.URL, Email: "e@x.com", APIToken: "tok"})
	if out.OK {
		t.Fatalf("ok = true, want false for zero visible projects")
	}
	if !strings.Contains(strings.ToLower(out.Error), "no projects") {
		t.Errorf("error = %q, want an empty-projects message", out.Error)
	}
	if out.Hint == "" {
		t.Errorf("hint empty, want an empty-projects hint")
	}
}

// TestTrackerTestConnectionOverlayUsesStoredCreds proves the empty-secret overlay:
// a request that omits the Jira credentials but names a repo resolves the stored
// values from the repo's config, and the probe authenticates with the stored token.
func TestTrackerTestConnectionOverlayUsesStoredCreds(t *testing.T) {
	var gotAuth string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/search/approximate-count":
			_, _ = io.WriteString(w, `{"count":3}`)
		case "/rest/api/3/project/search":
			_, _ = io.WriteString(w, `{"values":[{"key":"OPS","name":"Ops"}],"isLast":true,"total":1}`)
		}
	}))
	defer fake.Close()

	repoRoot := t.TempDir()
	ini := "TRACKER_PROVIDER=jira\nJIRA_BASE_URL=" + fake.URL + "\nJIRA_EMAIL=stored@acme.com\nJIRA_API_TOKEN=stored-token\n"
	if err := os.WriteFile(filepath.Join(repoRoot, config.ProjectConfigName), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	s := New("1.2.3", "127.0.0.1", "", []string{repoRoot}, false, testStoresAt(t, home))
	s.home = home
	ts := startConn(t, s)

	_, out := postConn(t, ts, "jira", TestConnectionRequest{Repo: filepath.Base(repoRoot)})
	if !out.OK {
		t.Fatalf("ok = false, resp = %+v", out)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("stored@acme.com:stored-token"))
	if gotAuth != wantAuth {
		t.Errorf("auth = %q, want the stored credentials %q", gotAuth, wantAuth)
	}
}

// TestTrackerTestConnectionNeverEchoesSecret guards the invariant that a secret
// value never appears in the response, even on failure.
func TestTrackerTestConnectionNeverEchoesSecret(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer fake.Close()

	s := connServer(t)
	ts := startConn(t, s)

	const secret = "super-secret-token-value"
	res := postJSON(t, ts.URL+APIPrefix+"/trackers/jira/test-connection",
		TestConnectionRequest{BaseURL: fake.URL, Email: "e@x.com", APIToken: secret})
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), secret) {
		t.Errorf("response body echoed the secret token: %s", body)
	}
}

func TestTrackerTestConnectionInternalTriviallyOK(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	_, out := postConn(t, ts, "internal", TestConnectionRequest{})
	if !out.OK {
		t.Errorf("internal ok = false, want true")
	}
	if len(out.Teams) != 0 || out.Error != "" {
		t.Errorf("internal response = %+v, want a bare ok", out)
	}
}

func TestTrackerTestConnectionInternalIgnoresBody(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	res, err := http.Post(ts.URL+APIPrefix+"/trackers/internal/test-connection", "application/json",
		strings.NewReader("not json at all }{"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for internal regardless of body", res.StatusCode)
	}
	var out TestConnectionResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !out.OK {
		t.Errorf("internal ok = false, want true for a garbage body")
	}
}

func TestTrackerTestConnectionUnsupportedProvider(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	res, _ := postConn(t, ts, "github", TestConnectionRequest{})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestTrackerTestConnectionMethodNotAllowed(t *testing.T) {
	s := connServer(t)
	ts := startConn(t, s)

	res, err := http.Get(ts.URL + APIPrefix + "/trackers/jira/test-connection")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}

func TestTrackerTestConnectionExposedBindRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	s := New("1.2.3", "0.0.0.0", "tok", nil, false, testStoresAt(t, home))
	s.home = home
	ts := startConn(t, s)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+APIPrefix+"/trackers/jira/test-connection", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 on an exposed bind without SERVE_ALLOW_REGISTER", res.StatusCode)
	}
}
