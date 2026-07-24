package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/prompts"
	"github.com/RomkaLTU/trau/internal/registry"
)

func newPromptTestServer(t *testing.T) (*httptest.Server, *hubstore.Stores) {
	t.Helper()
	stores := testStores(t)
	repo := registry.Repo{Name: "acme", Root: "/repo/acme", RunsDir: "/repo/acme/.trau/runs"}
	if err := stores.Registrations().Remember([]registry.Repo{repo}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}
	ts := httptest.NewServer(New("test", "127.0.0.1", "", nil, false, stores).Handler())
	t.Cleanup(ts.Close)
	return ts, stores
}

func putPrompt(t *testing.T, ts *httptest.Server, path, body string) (*http.Response, string) {
	t.Helper()
	payload, err := json.Marshal(PromptWriteRequest{Body: body})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, ts.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build PUT %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	out, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read %s body: %v", path, err)
	}
	return res, string(out)
}

func getPrompts(t *testing.T, ts *httptest.Server) []PromptView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/prompts")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET prompts status = %d (%s)", res.StatusCode, body)
	}
	var out PromptsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode prompts: %v", err)
	}
	return out.Prompts
}

func getRepoPrompts(t *testing.T, ts *httptest.Server, repo string) []RepoPromptView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos/"+repo+"/prompts")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET repo prompts status = %d (%s)", res.StatusCode, body)
	}
	var out RepoPromptsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode repo prompts: %v", err)
	}
	return out.Prompts
}

func findPrompt(t *testing.T, views []PromptView, name string) PromptView {
	t.Helper()
	for _, v := range views {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("prompt %q missing from catalog", name)
	return PromptView{}
}

func findRepoPrompt(t *testing.T, views []RepoPromptView, name string) RepoPromptView {
	t.Helper()
	for _, v := range views {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("prompt %q missing from repo catalog", name)
	return RepoPromptView{}
}

func TestPromptsCatalog(t *testing.T) {
	ts, _ := newPromptTestServer(t)

	views := getPrompts(t, ts)
	if len(views) != len(prompts.Catalog()) {
		t.Fatalf("catalog size = %d, want %d", len(views), len(prompts.Catalog()))
	}
	build := findPrompt(t, views, "build")
	if build.Title != "Build" || build.Default == "" {
		t.Fatalf("build entry = %+v, want title and default body", build)
	}
	if build.Override != nil {
		t.Fatalf("build override = %q, want null while unset", *build.Override)
	}
	var id PromptPlaceholderView
	for _, ph := range build.Placeholders {
		if ph.Name == "ID" {
			id = ph
		}
	}
	if !id.Required || id.Description == "" {
		t.Fatalf("build ID placeholder = %+v, want required with a description", id)
	}
}

func TestPromptOverrideRoundTrip(t *testing.T) {
	ts, _ := newPromptTestServer(t)
	body := "Stage and commit the work for {{.ID}} in one commit."

	res, out := putPrompt(t, ts, APIPrefix+"/prompts/commit", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", res.StatusCode, out)
	}
	var saved PromptView
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatalf("decode saved view: %v", err)
	}
	if saved.Override == nil || *saved.Override != body {
		t.Fatalf("saved override = %v, want the submitted body", saved.Override)
	}

	commit := findPrompt(t, getPrompts(t, ts), "commit")
	if commit.Override == nil || *commit.Override != body {
		t.Fatalf("catalog override = %v, want the stored body", commit.Override)
	}

	res, _ = deleteReq(t, ts, APIPrefix+"/prompts/commit")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", res.StatusCode)
	}
	if commit := findPrompt(t, getPrompts(t, ts), "commit"); commit.Override != nil {
		t.Fatalf("override survived reset: %q", *commit.Override)
	}

	res, _ = deleteReq(t, ts, APIPrefix+"/prompts/commit")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE of absent override status = %d, want 204", res.StatusCode)
	}
}

func TestPromptOverrideUnknownName404(t *testing.T) {
	ts, _ := newPromptTestServer(t)

	res, _ := putPrompt(t, ts, APIPrefix+"/prompts/nope", "anything")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT unknown prompt status = %d, want 404", res.StatusCode)
	}
	res, _ = deleteReq(t, ts, APIPrefix+"/prompts/nope")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown prompt status = %d, want 404", res.StatusCode)
	}
	res, _ = putPrompt(t, ts, APIPrefix+"/repos/acme/prompts/nope", "anything")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("repo PUT unknown prompt status = %d, want 404", res.StatusCode)
	}
}

func TestPromptOverrideValidation422(t *testing.T) {
	ts, stores := newPromptTestServer(t)

	res, body := putPrompt(t, ts, APIPrefix+"/prompts/verify", "Verify {{.ID}} and stop.")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("dropped-placeholder PUT status = %d (%s), want 422", res.StatusCode, body)
	}
	var fail map[string]string
	if err := json.Unmarshal([]byte(body), &fail); err != nil {
		t.Fatalf("decode 422 body: %v", err)
	}
	if !bytes.Contains([]byte(fail["error"]), []byte("{{.Verdict}}")) {
		t.Fatalf("422 message %q does not name the missing placeholder", fail["error"])
	}

	res, body = putPrompt(t, ts, APIPrefix+"/prompts/verify", "Verify {{.ID and stop.")
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unparsable PUT status = %d (%s), want 422", res.StatusCode, body)
	}

	scope, err := stores.Prompts().Scope("")
	if err != nil {
		t.Fatalf("Scope: %v", err)
	}
	if _, ok := scope["verify"]; ok {
		t.Fatal("a rejected override was stored")
	}
}

func TestRepoPromptsEffectivePrecedence(t *testing.T) {
	ts, _ := newPromptTestServer(t)

	build := findRepoPrompt(t, getRepoPrompts(t, ts, "acme"), "build")
	if build.Effective != "default" || build.EffectiveBody != build.Default {
		t.Fatalf("untouched entry effective = %q, want default with the built-in body", build.Effective)
	}

	globalBody := "Implement {{.ID}} on {{.Branch}} the global way. {{.SkillsNote}}"
	if res, out := putPrompt(t, ts, APIPrefix+"/prompts/build", globalBody); res.StatusCode != http.StatusOK {
		t.Fatalf("global PUT status = %d (%s)", res.StatusCode, out)
	}
	build = findRepoPrompt(t, getRepoPrompts(t, ts, "acme"), "build")
	if build.Effective != "global" || build.EffectiveBody != globalBody {
		t.Fatalf("entry after global PUT = %q/%q, want global body", build.Effective, build.EffectiveBody)
	}
	if build.RepoOverride != nil {
		t.Fatalf("repo override = %q, want null while unset", *build.RepoOverride)
	}

	repoBody := "Implement {{.ID}} on {{.Branch}} the acme way. {{.SkillsNote}}"
	res, out := putPrompt(t, ts, APIPrefix+"/repos/acme/prompts/build", repoBody)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("repo PUT status = %d (%s)", res.StatusCode, out)
	}
	var saved RepoPromptView
	if err := json.Unmarshal([]byte(out), &saved); err != nil {
		t.Fatalf("decode saved repo view: %v", err)
	}
	if saved.Effective != "repo" || saved.EffectiveBody != repoBody {
		t.Fatalf("saved repo view = %q/%q, want the repo body to win", saved.Effective, saved.EffectiveBody)
	}
	if saved.RepoOverride == nil || *saved.RepoOverride != repoBody {
		t.Fatalf("saved repo override = %v, want the submitted body", saved.RepoOverride)
	}

	if res, _ := deleteReq(t, ts, APIPrefix+"/repos/acme/prompts/build"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("repo DELETE status = %d, want 204", res.StatusCode)
	}
	build = findRepoPrompt(t, getRepoPrompts(t, ts, "acme"), "build")
	if build.Effective != "global" || build.EffectiveBody != globalBody {
		t.Fatalf("entry after repo reset = %q/%q, want inheritance back to global", build.Effective, build.EffectiveBody)
	}

	if res, _ := deleteReq(t, ts, APIPrefix+"/prompts/build"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("global DELETE status = %d, want 204", res.StatusCode)
	}
	build = findRepoPrompt(t, getRepoPrompts(t, ts, "acme"), "build")
	if build.Effective != "default" || build.EffectiveBody != build.Default {
		t.Fatalf("entry after global reset = %q, want inheritance back to the default", build.Effective)
	}
}

func TestRepoPromptsUnknownRepo404(t *testing.T) {
	ts, _ := newPromptTestServer(t)
	res, _ := get(t, ts, APIPrefix+"/repos/ghost/prompts")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET unknown repo prompts status = %d, want 404", res.StatusCode)
	}
}
