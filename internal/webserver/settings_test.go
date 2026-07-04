package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/registry"
)

// seedConfigRepo registers one repo pointing at a real on-disk root so the
// settings surface can read and write its <root>/.trau.ini. It isolates the OS
// home so the user layer never touches the developer's real ~/.trau.ini.
func seedConfigRepo(t *testing.T, home, name string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo root: %v", err)
	}
	repo := registry.Repo{Name: name, Root: root, RunsDir: filepath.Join(root, ".trau", "runs")}
	data, err := json.Marshal(map[string]registry.Repo{root: repo})
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "repos.json"), data, 0o644); err != nil {
		t.Fatalf("seed repos.json: %v", err)
	}
	return root
}

func getConfig(t *testing.T, ts *httptest.Server, repo string) (ConfigResponse, string) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/repos/" + repo + "/config")
	if err != nil {
		t.Fatalf("GET config: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.StatusCode, body)
	}
	var out ConfigResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return out, string(body)
}

func putConfig(t *testing.T, ts *httptest.Server, repo string, req ConfigWriteRequest) *http.Response {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/repos/"+repo+"/config", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("PUT config: %v", err)
	}
	return res
}

func findKey(keys []ConfigKeyView, key string) (ConfigKeyView, bool) {
	for _, v := range keys {
		if v.Key == key {
			return v, true
		}
	}
	return ConfigKeyView{}, false
}

func mustKey(t *testing.T, keys []ConfigKeyView, key string) ConfigKeyView {
	t.Helper()
	v, ok := findKey(keys, key)
	if !ok {
		t.Fatalf("key %q not in response", key)
	}
	return v
}

// TestConfigProvenance is the contract for the provenance display: every known
// key resolves to its effective value with the layer that supplied it — a
// project-file value reads from the project layer, an untouched key from the
// default layer — and the whitelist flags which keys the surface may edit.
func TestConfigProvenance(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	for _, k := range []string{"MAX_ITERATIONS", "TRAU_MAX_ITERATIONS", "MAX_REPAIRS", "TRAU_MAX_REPAIRS", "CLEANUP", "TRAU_CLEANUP"} {
		t.Setenv(k, "")
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("MAX_ITERATIONS=7\nCLEANUP=0\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}

	ts := instancesServer(t, home)
	out, _ := getConfig(t, ts, "acme")

	if out.Repo != "acme" {
		t.Errorf("repo = %q, want acme", out.Repo)
	}
	if strings.Join(out.Layers, ",") != "project,user" {
		t.Errorf("layers = %v, want [project user]", out.Layers)
	}

	iter := mustKey(t, out.Keys, "MAX_ITERATIONS")
	if iter.Value != "7" || iter.Layer != "project" {
		t.Errorf("MAX_ITERATIONS = %q@%q, want 7@project", iter.Value, iter.Layer)
	}
	if !iter.Editable {
		t.Errorf("MAX_ITERATIONS should be editable")
	}

	repairs := mustKey(t, out.Keys, "MAX_REPAIRS")
	if repairs.Value != "2" || repairs.Layer != "default" {
		t.Errorf("MAX_REPAIRS = %q@%q, want 2@default", repairs.Value, repairs.Layer)
	}

	bin := mustKey(t, out.Keys, "CLAUDE_BIN")
	if bin.Editable {
		t.Errorf("CLAUDE_BIN should be read-only over the settings surface")
	}
}

// TestConfigWriteProjectLayer covers the write path: a whitelisted edit lands in
// the chosen layer's file and a loop's own loader reads it back — the settings
// change a subsequent loop picks up.
func TestConfigWriteProjectLayer(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	t.Setenv("MAX_ITERATIONS", "")
	t.Setenv("TRAU_MAX_ITERATIONS", "")

	ts := instancesServer(t, home)
	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "MAX_ITERATIONS", Value: "9", Layer: "project"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", res.StatusCode)
	}
	var view ConfigKeyView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Value != "9" || view.Layer != "project" {
		t.Errorf("returned view = %q@%q, want 9@project", view.Value, view.Layer)
	}

	cfg, err := config.LoadLayered(config.ProjectConfigPath(root), "", "", "")
	if err != nil {
		t.Fatalf("reload as a loop would: %v", err)
	}
	if cfg.MaxIterations != 9 {
		t.Errorf("loop-loaded MaxIterations = %d, want 9", cfg.MaxIterations)
	}
}

// TestConfigWriteUserLayer covers writing the user layer: the value lands in the
// isolated ~/.trau.ini, not the repo's project file.
func TestConfigWriteUserLayer(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")

	ts := instancesServer(t, home)
	res := putConfig(t, ts, "acme", ConfigWriteRequest{Key: "THEME", Value: "nord", Layer: "user"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", res.StatusCode)
	}

	userHome, _ := os.UserHomeDir()
	userData, err := os.ReadFile(config.ProjectConfigPath(userHome))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userData), "THEME=nord") {
		t.Errorf("user config = %q, want it to hold THEME=nord", userData)
	}
	if _, err := os.Stat(config.ProjectConfigPath(root)); !os.IsNotExist(err) {
		t.Errorf("project config should be untouched by a user-layer write, stat err = %v", err)
	}
}

// TestConfigWriteRejections covers every guard on the write path: unknown keys,
// read-only keys, secrets, bad layers, and values a loop couldn't use are all
// refused without touching disk.
func TestConfigWriteRejections(t *testing.T) {
	cases := []struct {
		name string
		req  ConfigWriteRequest
		want int
	}{
		{"unknown key", ConfigWriteRequest{Key: "NOT_A_KEY", Value: "x", Layer: "project"}, http.StatusBadRequest},
		{"read-only key", ConfigWriteRequest{Key: "CLAUDE_BIN", Value: "/bin/sh", Layer: "project"}, http.StatusForbidden},
		{"secret key", ConfigWriteRequest{Key: "LINEAR_API_KEY", Value: "sk-nope", Layer: "user"}, http.StatusForbidden},
		{"bad layer", ConfigWriteRequest{Key: "NOTIFY", Value: "1", Layer: "env"}, http.StatusBadRequest},
		{"bad bool value", ConfigWriteRequest{Key: "NOTIFY", Value: "yes", Layer: "project"}, http.StatusBadRequest},
		{"bad option value", ConfigWriteRequest{Key: "MERGE_METHOD", Value: "octopus", Layer: "project"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := seedConfigRepo(t, home, "acme")
			ts := instancesServer(t, home)

			res := putConfig(t, ts, "acme", tc.req)
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
			if _, err := os.Stat(config.ProjectConfigPath(root)); !os.IsNotExist(err) {
				t.Errorf("a rejected write must not create the project config, stat err = %v", err)
			}
		})
	}
}

// TestConfigSecretRedaction is the contract for credential handling: a secret's
// value never appears in a response body — only whether it is set and the layer
// it came from — and the key is read-only.
func TestConfigSecretRedaction(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	const secret = "sk-live-do-not-leak"
	t.Setenv("LINEAR_API_KEY", secret)
	t.Setenv("SERVE_TOKEN", "")
	t.Setenv("TRAU_SERVE_TOKEN", "")

	ts := instancesServer(t, home)
	out, raw := getConfig(t, ts, "acme")

	if strings.Contains(raw, secret) {
		t.Fatalf("secret value leaked into the response body: %s", raw)
	}

	set := mustKey(t, out.Keys, "LINEAR_API_KEY")
	if !set.Secret {
		t.Errorf("LINEAR_API_KEY should be flagged secret")
	}
	if set.Value != "" {
		t.Errorf("secret value = %q, want empty", set.Value)
	}
	if !set.Set {
		t.Errorf("a set secret should report set=true")
	}
	if set.Layer != "env var" {
		t.Errorf("secret layer = %q, want env var provenance", set.Layer)
	}
	if set.Editable {
		t.Errorf("secret keys must be read-only")
	}

	unset := mustKey(t, out.Keys, "SERVE_TOKEN")
	if unset.Set {
		t.Errorf("an unset secret should report set=false")
	}
	if unset.Layer != "default" {
		t.Errorf("unset secret layer = %q, want default", unset.Layer)
	}
}

// TestConfigUnknownRepo404 keeps a repo the hub never saw a JSON 404, not the SPA
// shell.
func TestConfigUnknownRepo404(t *testing.T) {
	ts := instancesServer(t, t.TempDir())
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/config")
	if err != nil {
		t.Fatalf("GET config: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

// TestConfigRejectsPost keeps the resource to GET reads and PUT edits.
func TestConfigRejectsPost(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	ts := instancesServer(t, home)
	res, err := http.Post(ts.URL+APIPrefix+"/repos/acme/config", "application/json", nil)
	if err != nil {
		t.Fatalf("POST config: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", res.StatusCode)
	}
}
