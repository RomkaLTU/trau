package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// azureBoardFixture answers the four routes a status-options read makes for a
// project whose Platform team keeps two boards: a Stories board with a column the
// Features board does not carry, and a shared Done column each maps to a
// different state.
func azureBoardFixture() map[string]string {
	return map[string]string{
		"/Contoso/Platform/_apis/work/backlogconfiguration": `{
			"portfolioBacklogs":[{"rank":1,"defaultWorkItemType":{"name":"Feature"},"workItemTypes":[{"name":"Feature"}]}],
			"requirementBacklog":{"defaultWorkItemType":{"name":"User Story"},"workItemTypes":[{"name":"User Story"}]},
			"taskBacklog":{"defaultWorkItemType":{"name":"Task"},"workItemTypes":[{"name":"Task"}]},
			"bugWorkItems":{"defaultWorkItemType":{"name":"Bug"},"workItemTypes":[{"name":"Bug"}]}}`,
		"/Contoso/Platform/_apis/work/teamsettings": `{"bugsBehavior":"asRequirements"}`,
		"/Contoso/_apis/wit/workitemtypes/User Story/states": `{"value":[
			{"name":"New","category":"Proposed"},
			{"name":"Approved","category":"Proposed"},
			{"name":"Active","category":"InProgress"},
			{"name":"Resolved","category":"Resolved"},
			{"name":"Closed","category":"Completed"},
			{"name":"Removed","category":"Removed"}]}`,
		"/Contoso/Platform/_apis/work/boards": `{"value":[
			{"id":"stories","name":"Stories"},{"id":"features","name":"Features"}]}`,
		"/Contoso/Platform/_apis/work/boards/stories/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"User Story":"New"}},
			{"name":"Approved","columnType":"inProgress","stateMappings":{"User Story":"Approved"}},
			{"name":"Ready to test","columnType":"inProgress","stateMappings":{"User Story":"Resolved"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"User Story":"Closed"}}]}`,
		"/Contoso/Platform/_apis/work/boards/features/columns": `{"value":[
			{"name":"New","columnType":"incoming","stateMappings":{"Feature":"New"}},
			{"name":"Done","columnType":"outgoing","stateMappings":{"Feature":"Removed"}}]}`,
	}
}

// statusOptionsServer wires a hub whose "acme" repo points at a fake Azure DevOps
// organization, with the repo's own INI lines appended to the azure credentials.
func statusOptionsServer(t *testing.T, routes map[string]string, ini string) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()

	azure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected azure request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if body == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(azure.Close)

	runsDir := seedRepo(t, home, "acme")
	root := filepath.Dir(filepath.Dir(runsDir))
	writeRepoINI(t, root, "TRACKER_PROVIDER=azure\nAZURE_ORG_URL="+azure.URL+"\nAZURE_PAT=pat\n"+ini)

	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getStatusOptions(t *testing.T, ts *httptest.Server, repo string) (*http.Response, StatusOptionsResponse) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/tracker/status-options")
	if err != nil {
		t.Fatalf("GET status-options: %v", err)
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

// The union of both boards, deduped by name in board order, each column carrying
// the group its own state mappings suggest: Approved is the state STATUS_TODO
// pins, so it reads as unstarted and the other Proposed column falls to backlog.
func TestStatusOptionsUnionsBoardColumnsAndSuggestsGroups(t *testing.T) {
	ts := statusOptionsServer(t, azureBoardFixture(),
		"PROJECT=Contoso\nAZURE_TEAMS=Platform\nSTATUS_TODO=Approved\n")

	res, out := getStatusOptions(t, ts, "acme")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if out.Error != "" {
		t.Fatalf("error = %q, want a clean read", out.Error)
	}
	if out.Provider != "azure" {
		t.Errorf("provider = %q, want azure", out.Provider)
	}
	want := []StatusColumn{
		{Name: "New", SuggestedGroup: "backlog"},
		{Name: "Approved", SuggestedGroup: "unstarted"},
		{Name: "Ready to test", SuggestedGroup: "started"},
		{Name: "Done", SuggestedGroup: "done"},
	}
	if len(out.Grouping) != len(want) {
		t.Fatalf("grouping = %+v, want %+v", out.Grouping, want)
	}
	for i, col := range want {
		if out.Grouping[i] != col {
			t.Errorf("grouping[%d] = %+v, want %+v", i, out.Grouping[i], col)
		}
	}
	if len(out.PinOptions) != 6 || out.PinOptions[0].Name != "New" || out.PinOptions[0].Category != "Proposed" {
		t.Errorf("pinOptions = %+v, want the work-item type's own states with categories", out.PinOptions)
	}
	for _, pin := range out.PinOptions {
		if pin.Name == "Ready to test" {
			t.Error("pinOptions carries a board column; pins name states only (ADR 0036)")
		}
	}
}

// A repo on a provider with no mapping editor answers 404 so the settings page
// keeps rendering its generic rows.
func TestStatusOptionsIs404ForOtherProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	writeRepoINI(t, filepath.Dir(filepath.Dir(runsDir)), "TRACKER_PROVIDER=linear\nLINEAR_TEAM=COD\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, _ := getStatusOptions(t, ts, "acme")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a provider with no status-mapping options", res.StatusCode)
	}
	if res, _ := getStatusOptions(t, ts, "nope"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}
}

// A rejected PAT keeps the 200 and carries the remediation hint: the editor has
// the repo's own mapping to fall back on, and the operator needs to be told the
// token is the problem.
func TestStatusOptionsReportsRejectedCredentialsWithAHint(t *testing.T) {
	routes := azureBoardFixture()
	routes["/Contoso/Platform/_apis/work/backlogconfiguration"] = ""
	ts := statusOptionsServer(t, routes, "PROJECT=Contoso\nAZURE_TEAMS=Platform\n")

	res, out := getStatusOptions(t, ts, "acme")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the failure in the body", res.StatusCode)
	}
	if out.Error == "" {
		t.Fatal("error is empty, want the provider failure verbatim")
	}
	if !strings.Contains(out.Hint, "personal access token") {
		t.Errorf("hint = %q, want the rejected-PAT remediation", out.Hint)
	}
	if out.Grouping == nil || out.PinOptions == nil {
		t.Error("grouping/pinOptions are null, want empty lists so the editor can render its fallback")
	}
}

// Missing credentials never reach the network: the gap is named directly, the way
// the connection test names it.
func TestStatusOptionsNamesMissingCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	runsDir := seedRepo(t, home, "acme")
	writeRepoINI(t, filepath.Dir(filepath.Dir(runsDir)), "TRACKER_PROVIDER=azure\nPROJECT=Contoso\n")
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, out := getStatusOptions(t, ts, "acme")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(out.Error, "organization URL") || !strings.Contains(out.Error, "personal access token") {
		t.Errorf("error = %q, want both missing credentials named", out.Error)
	}
}
