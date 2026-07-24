package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubstore"
)

func skillsServer(t *testing.T, home string) *Server {
	t.Helper()
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	return s
}

func writeSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func getSkills(t *testing.T, ts *httptest.Server, repo string) SkillsResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/skills")
	if err != nil {
		t.Fatalf("GET skills: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.StatusCode, body)
	}
	var out SkillsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode skills: %v", err)
	}
	return out
}

// TestSkillsReadiness is the contract for the readiness snapshot: the detected
// project type, the installed skills enriched with lockfile provenance, the
// recommended starters still missing, and the repo's REQUIRED_SKILLS pins.
func TestSkillsReadiness(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module acme\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSkill(t, root, "golang-code-style")
	writeSkill(t, root, "hand-rolled")
	lock := `{"version":1,"skills":{"golang-code-style":{"source":"samber/cc-skills-golang","sourceType":"github","skillPath":"skills/golang-code-style/SKILL.md","computedHash":"abc"}}}`
	if err := os.WriteFile(filepath.Join(root, "skills-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("REQUIRED_SKILLS=golang-code-style,golang-error-handling\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)
	out := getSkills(t, ts, "acme")

	if out.ProjectType != "go" {
		t.Errorf("project_type = %q, want go", out.ProjectType)
	}

	byName := map[string]SkillView{}
	for _, v := range out.Installed {
		byName[v.Name] = v
	}
	pinned, ok := byName["golang-code-style"]
	if !ok {
		t.Fatalf("installed missing golang-code-style: %+v", out.Installed)
	}
	if pinned.Source != "samber/cc-skills-golang" || pinned.SourceType != "github" || !pinned.Pinned {
		t.Errorf("golang-code-style provenance = %+v, want lockfile source + pinned", pinned)
	}
	bare, ok := byName["hand-rolled"]
	if !ok {
		t.Fatalf("installed missing hand-rolled: %+v", out.Installed)
	}
	if bare.Source != "" || bare.Pinned {
		t.Errorf("hand-rolled = %+v, want no provenance and unpinned", bare)
	}

	var recNames []string
	for _, r := range out.Recommended {
		recNames = append(recNames, r.Name)
	}
	if contains(recNames, "golang-code-style") {
		t.Errorf("recommended should exclude the installed golang-code-style: %v", recNames)
	}
	if !contains(recNames, "golang-error-handling") {
		t.Errorf("recommended should include the missing golang-error-handling: %v", recNames)
	}

	if len(out.Required) != 2 || out.Required[0] != "golang-code-style" || out.Required[1] != "golang-error-handling" {
		t.Errorf("required = %v, want the REQUIRED_SKILLS pins", out.Required)
	}
}

func TestSkillsUnknownRepo404(t *testing.T) {
	ts := httptest.NewServer(skillsServer(t, t.TempDir()).Handler())
	t.Cleanup(ts.Close)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/skills")
	if err != nil {
		t.Fatalf("GET skills: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestSkillsSearchProxy is the contract for the registry proxy: skills.sh hits
// are passed through with a resolved page URL, and identical queries are served
// from the short cache rather than re-hitting the registry.
func TestSkillsSearchProxy(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	var hits atomic.Int64
	var lastQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastQuery = r.URL.Query().Encode()
		_, _ = io.WriteString(w, `{"query":"golang","skills":[{"id":"samber/cc-skills-golang/golang-code-style","skillId":"golang-code-style","name":"golang-code-style","installs":34209,"source":"samber/cc-skills-golang"}],"count":1}`)
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("SKILLS_API_URL", upstream.URL)

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	out := searchSkills(t, ts, "acme", "q=golang&owner=samber")
	if out.Unavailable {
		t.Fatalf("search should be available: %+v", out)
	}
	if len(out.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(out.Results))
	}
	got := out.Results[0]
	if got.SkillID != "golang-code-style" || got.Installs != 34209 || got.Source != "samber/cc-skills-golang" {
		t.Errorf("passthrough = %+v, want the registry fields", got)
	}
	if got.URL != "https://skills.sh/samber/cc-skills-golang/golang-code-style" {
		t.Errorf("url = %q, want the skills.sh page", got.URL)
	}
	if !strings.Contains(lastQuery, "limit=10") || !strings.Contains(lastQuery, "owner=samber") {
		t.Errorf("upstream query = %q, want limit=10 and owner passed through", lastQuery)
	}

	searchSkills(t, ts, "acme", "q=golang&owner=samber")
	if hits.Load() != 1 {
		t.Errorf("registry hits = %d, want 1 — the second identical query should hit the cache", hits.Load())
	}
}

func TestSkillsSearchEmptyQuery(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	t.Setenv("SKILLS_API_URL", "http://127.0.0.1:0")

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	out := searchSkills(t, ts, "acme", "q=")
	if out.Unavailable || len(out.Results) != 0 {
		t.Errorf("empty query = %+v, want an available empty result without hitting the registry", out)
	}
}

// TestSkillsSearchUnavailable covers both degradation paths — a registry that
// errors and one that cannot be reached — returning an explicit unavailable
// result instead of a 500 that would break the panel.
func TestSkillsSearchUnavailable(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close()

	cases := map[string]string{"registry errors": failing.URL, "registry unreachable": unreachableURL}
	t.Cleanup(failing.Close)
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SKILLS_API_URL", base)
			ts := httptest.NewServer(skillsServer(t, home).Handler())
			t.Cleanup(ts.Close)
			out := searchSkills(t, ts, "acme", "q=golang")
			if !out.Unavailable {
				t.Errorf("out = %+v, want unavailable=true", out)
			}
			if len(out.Results) != 0 {
				t.Errorf("results = %d, want 0 when the registry is down", len(out.Results))
			}
		})
	}
}

// TestSkillsInstall covers the install wrapper wiring: a successful install
// reflects in the follow-up snapshot, and a CLI failure surfaces its output as a
// readable error.
func TestSkillsInstall(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	s := skillsServer(t, home)
	s.installSkill = func(_ context.Context, repoRoot, pkg string) error {
		if pkg != "samber/cc-skills-golang@golang-performance" {
			t.Errorf("install pkg = %q", pkg)
		}
		writeSkill(t, repoRoot, "golang-performance")
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := postSkill(t, ts, "acme", `{"package":"samber/cc-skills-golang@golang-performance"}`)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("install status = %d, want 201 (%s)", res.StatusCode, body)
	}
	var snap SkillsResponse
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !containsInstalled(snap.Installed, "golang-performance") {
		t.Errorf("install snapshot missing the new skill: %+v", snap.Installed)
	}
	if !containsInstalled(getSkills(t, ts, "acme").Installed, "golang-performance") {
		t.Errorf("follow-up GET should reflect the install")
	}
}

func TestSkillsInstallError(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	s := skillsServer(t, home)
	s.installSkill = func(context.Context, string, string) error {
		return errString("install skill x: exit status 1: unknown source")
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	for _, tc := range []struct {
		name, body string
		want       int
		wantMsg    string
	}{
		{"cli failure", `{"package":"nope/nope@x"}`, http.StatusInternalServerError, "unknown source"},
		{"missing package", `{}`, http.StatusBadRequest, "package is required"},
		{"bad json", `not json`, http.StatusBadRequest, "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := postSkill(t, ts, "acme", tc.body)
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d (%s)", res.StatusCode, tc.want, body)
			}
			if !bytes.Contains(body, []byte(tc.wantMsg)) {
				t.Errorf("body = %s, want it to mention %q", body, tc.wantMsg)
			}
		})
	}
}

// TestSkillsRemove covers the remove wrapper wiring: a successful remove drops
// the skill from the follow-up snapshot, and a CLI failure surfaces its output.
func TestSkillsRemove(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	writeSkill(t, root, "golang-code-style")

	s := skillsServer(t, home)
	s.removeSkill = func(_ context.Context, repoRoot, name string) error {
		if name != "golang-code-style" {
			t.Errorf("remove name = %q", name)
		}
		return os.RemoveAll(filepath.Join(repoRoot, ".agents", "skills", name))
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := deleteSkill(t, ts, "acme", "golang-code-style")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("remove status = %d, want 200 (%s)", res.StatusCode, body)
	}
	if containsInstalled(getSkills(t, ts, "acme").Installed, "golang-code-style") {
		t.Errorf("removed skill should be gone from the snapshot")
	}
}

func TestSkillsRemoveError(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	s := skillsServer(t, home)
	s.removeSkill = func(context.Context, string, string) error {
		return errString("remove skill x: exit status 1: not installed")
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := deleteSkill(t, ts, "acme", "ghost")
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", res.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("not installed")) {
		t.Errorf("body = %s, want it to surface the CLI output", body)
	}
}

func TestSkillsRemoveEmptyName(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")

	s := skillsServer(t, home)
	s.removeSkill = func(context.Context, string, string) error {
		t.Fatal("removeSkill should not run for an empty name")
		return nil
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	res := deleteSkill(t, ts, "acme", "")
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", res.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("skill name is required")) {
		t.Errorf("body = %s, want it to mention the missing name", body)
	}
}

// TestRequiredSkillsWritable is the settings-surface contract: the panel can
// pin REQUIRED_SKILLS through the config write path and the readiness snapshot
// reflects the new pins.
func TestRequiredSkillsWritable(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "REQUIRED_SKILLS", Value: "golang-code-style,golang-pro", Layer: "project"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("PUT REQUIRED_SKILLS status = %d, want 200 (%s)", res.StatusCode, body)
	}

	cfg, err := config.LoadLayered(config.ProjectConfigPath(root), "", "", "")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(cfg.RequiredSkills) != 2 || cfg.RequiredSkills[0] != "golang-code-style" {
		t.Errorf("loop-loaded RequiredSkills = %v, want the written pins", cfg.RequiredSkills)
	}

	out := getSkills(t, ts, "acme")
	if len(out.Required) != 2 || out.Required[1] != "golang-pro" {
		t.Errorf("skills.required = %v, want the written pins", out.Required)
	}
}

func searchSkills(t *testing.T, ts *httptest.Server, repo, rawQuery string) SkillsSearchResponse {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/skills/search?" + rawQuery)
	if err != nil {
		t.Fatalf("GET search: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search status = %d, want 200 (%s)", res.StatusCode, body)
	}
	var out SkillsSearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	return out
}

func postSkill(t *testing.T, ts *httptest.Server, repo, body string) *http.Response {
	t.Helper()
	res, err := http.Post(ts.URL+APIPrefix+"/repos/"+repo+"/skills", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST skill: %v", err)
	}
	return res
}

func deleteSkill(t *testing.T, ts *httptest.Server, repo, name string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+APIPrefix+"/repos/"+repo+"/skills/"+name, nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE skill: %v", err)
	}
	return res
}

func containsInstalled(xs []SkillView, want string) bool {
	for _, x := range xs {
		if x.Name == want {
			return true
		}
	}
	return false
}

type errString string

func (e errString) Error() string { return string(e) }

func writeSkillWithFrontmatter(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill %s: %v", name, err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func putSkillRules(t *testing.T, ts *httptest.Server, repo string, body string) (int, SkillsResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/repos/"+repo+"/skills/rules", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT rules: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	var out SkillsResponse
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// TestSkillsRoutingRules is the contract for the routing-rules surface: the
// snapshot carries each skill's manifest metadata and routing scope, the
// per-phase plan previews a run with no ticket (so auto rules read as
// non-matching), and a rules write round-trips through the hub.
func TestSkillsRoutingRules(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module acme\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSkillWithFrontmatter(t, root, "golang-code-style", "Golang code style conventions for reviewing code.")
	writeSkillWithFrontmatter(t, root, "web-feature", "Build a web UI feature end to end.")
	writeSkill(t, root, "github-release")
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "broken"), 0o755); err != nil {
		t.Fatalf("mkdir broken skill: %v", err)
	}

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	status, out := putSkillRules(t, ts, "acme", `{"rules":[
		{"skill":"golang-code-style","scope":"always"},
		{"skill":"web-feature","scope":"auto","phases":["build","verify"],"paths":["web/**"]},
		{"skill":"github-release","scope":"manual"}
	]}`)
	if status != http.StatusOK {
		t.Fatalf("PUT rules status = %d, want 200", status)
	}
	if len(out.Rules) != 3 || out.Rules[1].Skill != "web-feature" || out.Rules[1].Paths[0] != "web/**" {
		t.Fatalf("rules = %+v, want the three saved rules", out.Rules)
	}

	out = getSkills(t, ts, "acme")
	byName := map[string]SkillView{}
	for _, v := range out.Installed {
		byName[v.Name] = v
	}
	if got := byName["web-feature"]; got.Scope != "auto" || got.Description == "" || len(got.SuggestedKeywords) == 0 {
		t.Errorf("web-feature = %+v, want the auto scope and its manifest metadata", got)
	}
	if got := byName["github-release"]; got.Scope != "manual" {
		t.Errorf("github-release scope = %q, want manual", got.Scope)
	}
	if got := byName["broken"]; !got.Invalid {
		t.Errorf("broken = %+v, want it flagged invalid", got)
	}

	plan := map[string]SkillPlanView{}
	for _, p := range out.Plan {
		plan[p.Phase] = p
	}
	if len(plan) != 3 {
		t.Fatalf("plan = %+v, want one entry per phase", out.Plan)
	}
	for phase, p := range plan {
		if !contains(p.Skills, "golang-code-style") {
			t.Errorf("%s plan = %v, want the always skill", phase, p.Skills)
		}
		if contains(p.Skills, "web-feature") {
			t.Errorf("%s plan = %v, want the auto rule to read as non-matching without a ticket", phase, p.Skills)
		}
		if contains(p.Skills, "github-release") {
			t.Errorf("%s plan = %v, want the manual skill left out", phase, p.Skills)
		}
	}
}

func TestSkillRulesRejectsNamelessRule(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)

	if status, _ := putSkillRules(t, ts, "acme", `{"rules":[{"skill":"  ","scope":"always"}]}`); status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func seedSkillCall(t *testing.T, home, root, ticket, phase, provider, skills string, ts time.Time) {
	t.Helper()
	call := hubstore.TokenCall{
		Ticket:   ticket,
		TS:       ts.Format("2006-01-02T15:04:05"),
		Phase:    phase,
		Provider: provider,
		Skills:   skills,
	}
	if err := testStoresAt(t, home).Tokens().Append(root, []hubstore.TokenCall{call}); err != nil {
		t.Fatalf("append token call: %v", err)
	}
}

func seedPlannedSkills(t *testing.T, home, root, ticket, phase string, names []string, ts time.Time) {
	t.Helper()
	fields, err := json.Marshal(map[string]any{"ticket": ticket, "skills": names})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	ev := hubstore.NewEvent{
		TS:     ts.Format(time.RFC3339),
		Kind:   event.KindSkillsPlanned,
		Phase:  phase,
		Msg:    "planned skills: " + strings.Join(names, ", "),
		Fields: string(fields),
	}
	if _, err := testStoresAt(t, home).Events().Append(root, []hubstore.NewEvent{ev}); err != nil {
		t.Fatalf("append planned event: %v", err)
	}
}

// TestSkillsRunCoverage is the run-coverage contract: a skill a recent call
// reported loading carries its load count and timestamp, one no call named
// carries neither, and the plan a phase filed is paired with what its agent
// actually loaded.
func TestSkillsRunCoverage(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	writeSkill(t, root, "golang-cli")
	writeSkill(t, root, "web-feature")

	loaded := time.Now().Add(-24 * time.Hour)
	seedPlannedSkills(t, home, root, "COD-1", "build", []string{"golang-cli", "web-feature"}, loaded)
	seedSkillCall(t, home, root, "COD-1", "build", "claude", `["golang-cli"]`, loaded.Add(time.Minute))
	seedSkillCall(t, home, root, "COD-1", "build", "claude", `["old"]`, time.Now().Add(-90*24*time.Hour))

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)
	out := getSkills(t, ts, "acme")

	if !out.Coverage.HasData || out.Coverage.Days != skillCoverageDays {
		t.Fatalf("coverage = %+v, want evidence over the %d-day window", out.Coverage, skillCoverageDays)
	}
	byName := map[string]SkillView{}
	for _, v := range out.Installed {
		byName[v.Name] = v
	}
	if got := byName["golang-cli"]; got.Loads != 1 || got.LastLoaded == "" {
		t.Errorf("golang-cli = %+v, want one load inside the window with its timestamp", got)
	}
	if got := byName["web-feature"]; got.Loads != 0 || got.LastLoaded != "" {
		t.Errorf("web-feature = %+v, want no load evidence", got)
	}

	if len(out.Coverage.Phases) != 1 {
		t.Fatalf("phases = %+v, want the one planned attempt", out.Coverage.Phases)
	}
	phase := out.Coverage.Phases[0]
	if phase.Ticket != "COD-1" || phase.Phase != "build" || phase.Unknown {
		t.Fatalf("phase = %+v, want a recoverable build attempt for COD-1", phase)
	}
	if !contains(phase.Planned, "web-feature") || contains(phase.Loaded, "web-feature") {
		t.Errorf("phase = %+v, want web-feature planned but not loaded", phase)
	}
}

// TestSkillsCoverageWithoutReportingProvider holds the "no data" line: a repo
// whose runs used only providers that never report skill usage carries no
// evidence, so an unloaded skill must not read as one nobody uses.
func TestSkillsCoverageWithoutReportingProvider(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	writeSkill(t, root, "golang-cli")

	ran := time.Now().Add(-2 * time.Hour)
	seedPlannedSkills(t, home, root, "COD-2", "build", []string{"golang-cli"}, ran)
	seedSkillCall(t, home, root, "COD-2", "build", "codex", "", ran.Add(time.Minute))

	ts := httptest.NewServer(skillsServer(t, home).Handler())
	t.Cleanup(ts.Close)
	out := getSkills(t, ts, "acme")

	if out.Coverage.HasData {
		t.Errorf("coverage.HasData = true, want no recoverable evidence")
	}
	if !contains(out.Coverage.Silent, "codex") {
		t.Errorf("silent providers = %v, want codex named", out.Coverage.Silent)
	}
	if len(out.Coverage.Phases) != 1 || !out.Coverage.Phases[0].Unknown {
		t.Errorf("phases = %+v, want the attempt flagged unknown", out.Coverage.Phases)
	}
}
