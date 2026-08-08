package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
)

func registerRepoReq(t *testing.T, ts *httptest.Server, root string) {
	t.Helper()
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root})
	if body := readBody(t, res); res.StatusCode != http.StatusCreated {
		t.Fatalf("register %s = %d (%s)", root, res.StatusCode, body)
	}
}

func putProjectTrackerReq(t *testing.T, ts *httptest.Server, id string, keys map[string]string) (*http.Response, string) {
	t.Helper()
	res := putJSON(t, ts.URL+APIPrefix+"/projects/"+url.PathEscape(id)+"/tracker", ProjectTrackerRequest{Keys: keys})
	return res, readBody(t, res)
}

func decodeProjectTracker(t *testing.T, body string) ProjectTrackerResponse {
	t.Helper()
	var view ProjectTrackerResponse
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode project tracker: %v (body %q)", err, body)
	}
	return view
}

func repoINI(t *testing.T, root string) map[string]string {
	t.Helper()
	keys, err := config.ParseEnvFile(config.ProjectConfigPath(root))
	if err != nil {
		t.Fatalf("parse %s config: %v", root, err)
	}
	return keys
}

func wantINI(t *testing.T, root string, want map[string]string) {
	t.Helper()
	got := repoINI(t, root)
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s config %s = %q, want %q (file %v)", root, key, got[key], value, got)
		}
	}
}

// The onboarding promise: a two-repo project is asked for its tracker once, and
// both members end up able to run the CLI against it.
func TestProjectTrackerConfiguredOnceSeedsEveryMember(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}

	res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
		"TRACKER_PROVIDER": "linear",
		"LINEAR_TEAM":      "COD",
		"LINEAR_API_KEY":   "lin_secret",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}

	for _, root := range []string{api, web} {
		wantINI(t, root, map[string]string{
			"TRACKER_PROVIDER": "linear",
			"LINEAR_TEAM":      "COD",
			"LINEAR_API_KEY":   "lin_secret",
		})
	}

	for _, key := range decodeProjectTracker(t, body).Keys {
		if key.Key == "LINEAR_API_KEY" && (key.Value != "" || !key.Set) {
			t.Fatalf("secret key served as %+v, want no value and set=true", key)
		}
	}
}

// Azure DevOps travels as tracker identity like the Linear/Jira keys: the project
// is asked once and every member repo can run the CLI against the team project.
func TestProjectTrackerCarriesAzureKeys(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Contoso")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}

	want := map[string]string{
		"TRACKER_PROVIDER": "azure",
		"LINEAR_TEAM":      "Contoso",
		"AZURE_ORG_URL":    "https://dev.azure.com/contoso",
		"AZURE_PAT":        "azure_secret",
	}
	res, body := putProjectTrackerReq(t, ts, project.ID, want)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}

	for _, root := range []string{api, web} {
		wantINI(t, root, want)
	}

	for _, key := range decodeProjectTracker(t, body).Keys {
		if key.Key == "AZURE_PAT" && (key.Value != "" || !key.Set) {
			t.Fatalf("AZURE_PAT served as %+v, want no value and set=true", key)
		}
	}
}

// Explicit per-repo config outranks the project default, on the way in and on
// every later edit.
func TestProjectTrackerLeavesAMembersOwnKeysAlone(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	writeRepoINI(t, web, "TRACKER_PROVIDER=jira\nLINEAR_TEAM=MELGA\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}
	wantINI(t, web, map[string]string{"TRACKER_PROVIDER": "jira", "LINEAR_TEAM": "MELGA"})

	for _, team := range []string{"COD", "ENG"} {
		res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
			"TRACKER_PROVIDER": "linear",
			"LINEAR_TEAM":      team,
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
		}
		wantINI(t, api, map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": team})
		wantINI(t, web, map[string]string{"TRACKER_PROVIDER": "jira", "LINEAR_TEAM": "MELGA"})
	}
}

// A repo onboarded before projects owned the tracker keeps it with no prompt,
// and a repo joining it later inherits the same answers.
func TestProjectTrackerAdoptsALoneRepoConfig(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	writeRepoINI(t, api, "TRACKER_PROVIDER=linear\nLINEAR_TEAM=COD\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	res, body := get(t, ts, APIPrefix+"/projects/api/tracker")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get tracker = %d (%s)", res.StatusCode, body)
	}
	adopted := map[string]string{}
	for _, key := range decodeProjectTracker(t, body).Keys {
		adopted[key.Key] = key.Value
	}
	want := map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": "COD"}
	for key, value := range want {
		if adopted[key] != value {
			t.Fatalf("adopted %s = %q, want %q (all %v)", key, adopted[key], value, adopted)
		}
	}
	wantINI(t, api, want)

	if res, body := addProjectRepo(t, ts, "api", web); res.StatusCode != http.StatusOK {
		t.Fatalf("add %s = %d (%s)", web, res.StatusCode, body)
	}
	wantINI(t, web, want)
}

// Onboarding a single repo configures its project's tracker but writes the
// project-wide answers straight into the repo, so adoption has to pick up the
// keys the project is still missing for a second member to inherit them.
func TestProjectTrackerAdoptsTheKeysALoneRepoStillHolds(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	lone := gitRepo(t, base, "lone", "dir")
	joiner := gitRepo(t, base, "joiner", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, lone)

	if res, body := putProjectTrackerReq(t, ts, "lone", map[string]string{
		"TRACKER_PROVIDER": "internal",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}
	for key, value := range map[string]string{"READY_LABEL": "solo-label", "EPIC_FLOW": "1"} {
		res := putConfig(t, ts, "lone", ConfigWriteRequest{Key: key, Value: value, Layer: "project"})
		if body := readBody(t, res); res.StatusCode != http.StatusOK {
			t.Fatalf("put repo config %s = %d (%s)", key, res.StatusCode, body)
		}
	}

	res, body := get(t, ts, APIPrefix+"/projects/lone/tracker")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get tracker = %d (%s)", res.StatusCode, body)
	}
	adopted := map[string]string{}
	for _, key := range decodeProjectTracker(t, body).Keys {
		adopted[key.Key] = key.Value
	}
	want := map[string]string{
		"TRACKER_PROVIDER": "internal",
		"READY_LABEL":      "solo-label",
		"EPIC_FLOW":        "1",
	}
	for key, value := range want {
		if adopted[key] != value {
			t.Fatalf("adopted %s = %q, want %q (all %v)", key, adopted[key], value, adopted)
		}
	}

	registerRepoReq(t, ts, joiner)
	if res, body := addProjectRepo(t, ts, "lone", joiner); res.StatusCode != http.StatusOK {
		t.Fatalf("add %s = %d (%s)", joiner, res.StatusCode, body)
	}
	wantINI(t, joiner, want)
}

// Adoption rides the projects list, the read every screen makes, so an upgraded
// hub promotes a pre-epic repo's tracker to its project before a second repo
// joins it — with nothing but the app's own polling in between.
func TestProjectTrackerAdoptsOnTheProjectsList(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	writeRepoINI(t, api, "TRACKER_PROVIDER=linear\nLINEAR_TEAM=COD\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	if res, body := get(t, ts, APIPrefix+"/projects"); res.StatusCode != http.StatusOK {
		t.Fatalf("list projects = %d (%s)", res.StatusCode, body)
	}
	if res, body := addProjectRepo(t, ts, "api", web); res.StatusCode != http.StatusOK {
		t.Fatalf("add %s = %d (%s)", web, res.StatusCode, body)
	}
	wantINI(t, web, map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": "COD"})
}

// Grouping repos is not configuring a tracker: a project assembled one repo at a
// time never claims the first joiner's keys, so nothing of its own is copied to
// the others or overwritten by a later edit.
func TestProjectTrackerIgnoresMembersWhileGrouping(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	own := map[string]string{
		"TRACKER_PROVIDER": "jira",
		"JIRA_BASE_URL":    "https://only.atlassian.net",
		"JIRA_API_TOKEN":   "jira_secret",
	}
	writeRepoINI(t, api, "TRACKER_PROVIDER=jira\nJIRA_BASE_URL=https://only.atlassian.net\nJIRA_API_TOKEN=jira_secret\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	if res, body := get(t, ts, APIPrefix+"/projects/api/tracker"); res.StatusCode != http.StatusOK {
		t.Fatalf("get standalone tracker = %d (%s)", res.StatusCode, body)
	}

	project := createProjectReq(t, ts, "Combo")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}

	res, body := get(t, ts, APIPrefix+"/projects/"+project.ID+"/tracker")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get tracker = %d (%s)", res.StatusCode, body)
	}
	if keys := decodeProjectTracker(t, body).Keys; len(keys) > 0 {
		t.Fatalf("grouped project reports tracker %+v, want none configured", keys)
	}
	if keys := repoINI(t, web); len(keys) > 0 {
		t.Fatalf("grouping seeded %v into a repo the project never configured", keys)
	}

	if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
		"TRACKER_PROVIDER": "github",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}
	wantINI(t, api, own)
	wantINI(t, web, map[string]string{"TRACKER_PROVIDER": "github"})
}

// A key a member sets on its own settings page is the repo's from then on, even
// though the project seeded it first. The switch to the internal tracker is the
// one exception — it claims the provider back for the project, so that it cannot
// leave a member talking to the tracker the project just left.
func TestProjectTrackerLeavesAKeyTheRepoTookBack(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}
	if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
		"TRACKER_PROVIDER": "linear",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}

	res := putConfig(t, ts, "web", ConfigWriteRequest{Key: "TRACKER_PROVIDER", Value: "github", Layer: "project"})
	if body := readBody(t, res); res.StatusCode != http.StatusOK {
		t.Fatalf("put repo config = %d (%s)", res.StatusCode, body)
	}

	if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
		"TRACKER_PROVIDER": "linear",
		"LINEAR_TEAM":      "COD",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("second put tracker = %d (%s)", res.StatusCode, body)
	}
	wantINI(t, web, map[string]string{"TRACKER_PROVIDER": "github", "LINEAR_TEAM": "COD"})
	wantINI(t, api, map[string]string{"TRACKER_PROVIDER": "linear", "LINEAR_TEAM": "COD"})
}

// Switching the project's tracker clears the keys the old one left behind in the
// members that hold them on the project's behalf.
func TestProjectTrackerEditDropsKeysItNoLongerSets(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)

	if res, body := putProjectTrackerReq(t, ts, "api", map[string]string{
		"TRACKER_PROVIDER": "linear",
		"LINEAR_TEAM":      "COD",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
	}
	if res, body := putProjectTrackerReq(t, ts, "api", map[string]string{
		"TRACKER_PROVIDER": "internal",
		"LINEAR_TEAM":      "",
	}); res.StatusCode != http.StatusOK {
		t.Fatalf("second put tracker = %d (%s)", res.StatusCode, body)
	}

	wantINI(t, api, map[string]string{"TRACKER_PROVIDER": "internal"})
	if _, ok := repoINI(t, api)["LINEAR_TEAM"]; ok {
		t.Fatalf("LINEAR_TEAM survived the switch to internal (file %v)", repoINI(t, api))
	}
}

// READY_LABEL and EPIC_FLOW answer for the whole project, so configuring them
// once reaches every member — bar the one that already answered for itself, on
// the first seeding and on every later one.
func TestProjectTrackerSeedsReadyLabelAndEpicFlow(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	writeRepoINI(t, web, "READY_LABEL=web-only\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}

	var last string
	for _, flow := range []string{"1", "0"} {
		res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
			"TRACKER_PROVIDER": "internal",
			"READY_LABEL":      "ship-it",
			"EPIC_FLOW":        flow,
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
		}
		last = body
		wantINI(t, api, map[string]string{"READY_LABEL": "ship-it", "EPIC_FLOW": flow})
		wantINI(t, web, map[string]string{"READY_LABEL": "web-only", "EPIC_FLOW": flow})
	}

	stored := map[string]string{}
	for _, key := range decodeProjectTracker(t, last).Keys {
		stored[key.Key] = key.Value
	}
	if stored["READY_LABEL"] != "ship-it" || stored["EPIC_FLOW"] != "0" {
		t.Fatalf("project holds %v, want READY_LABEL=ship-it and EPIC_FLOW=0", stored)
	}
}

// Clearing the label at project level takes it out of the members that carry it
// on the project's behalf, and only those.
func TestProjectTrackerClearedReadyLabelSparesAMembersOwn(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	web := gitRepo(t, base, "web", "dir")
	writeRepoINI(t, web, "READY_LABEL=web-only\n")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)
	registerRepoReq(t, ts, web)

	project := createProjectReq(t, ts, "Platform")
	for _, root := range []string{api, web} {
		if res, body := addProjectRepo(t, ts, project.ID, root); res.StatusCode != http.StatusOK {
			t.Fatalf("add %s = %d (%s)", root, res.StatusCode, body)
		}
	}
	for _, label := range []string{"ship-it", ""} {
		if res, body := putProjectTrackerReq(t, ts, project.ID, map[string]string{
			"TRACKER_PROVIDER": "internal",
			"READY_LABEL":      label,
		}); res.StatusCode != http.StatusOK {
			t.Fatalf("put tracker = %d (%s)", res.StatusCode, body)
		}
	}

	if _, ok := repoINI(t, api)["READY_LABEL"]; ok {
		t.Fatalf("READY_LABEL survived being cleared (file %v)", repoINI(t, api))
	}
	wantINI(t, web, map[string]string{"READY_LABEL": "web-only"})
}

func TestProjectTrackerRefusesKeysOutsideTheSeededSet(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	api := gitRepo(t, base, "api", "dir")
	_, ts := controlServer(t, home, nil)
	registerRepoReq(t, ts, api)

	cases := []struct {
		name string
		keys map[string]string
	}{
		{"a key the project does not seed", map[string]string{"BASE_BRANCH": "develop"}},
		{"a value the key does not accept", map[string]string{"TRACKER_PROVIDER": "trello"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, body := putProjectTrackerReq(t, ts, "api", tc.keys)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("put tracker = %d (%s), want 400", res.StatusCode, body)
			}
			if _, ok := repoINI(t, api)["BASE_BRANCH"]; ok {
				t.Fatalf("refused write reached %s (file %v)", api, repoINI(t, api))
			}
		})
	}
}

func TestProjectTrackerRefusesAnUnknownProject(t *testing.T) {
	_, ts := controlServer(t, t.TempDir(), nil)

	res, body := get(t, ts, APIPrefix+"/projects/ghost/tracker")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("get tracker for a missing project = %d (%s), want 404", res.StatusCode, body)
	}
}
