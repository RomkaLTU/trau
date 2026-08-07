package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/config"
)

func getGlobalConfig(t *testing.T, ts *httptest.Server) (ConfigResponse, string) {
	t.Helper()
	res, err := http.Get(ts.URL + APIPrefix + "/config")
	if err != nil {
		t.Fatalf("GET global config: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.StatusCode, body)
	}
	var out ConfigResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode global config: %v", err)
	}
	return out, string(body)
}

func putGlobalConfig(t *testing.T, ts *httptest.Server, req ConfigWriteRequest) *http.Response {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPut, ts.URL+APIPrefix+"/config", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("PUT global config: %v", err)
	}
	return res
}

func writeUserConfig(t *testing.T, body string) string {
	t.Helper()
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	path := config.ProjectConfigPath(userHome)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed user config: %v", err)
	}
	return path
}

// TestGlobalConfigResolution is the contract for the global scope: every known
// key resolves against the built-in defaults and ~/.trau.ini alone. A key a repo
// sets in its own .trau.ini is not a global default, so it reads as unset here,
// and the only layer an edit may target is the user one.
func TestGlobalConfigResolution(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	for _, k := range []string{"MAX_ITERATIONS", "TRAU_MAX_ITERATIONS", "MAX_REPAIRS", "TRAU_MAX_REPAIRS"} {
		t.Setenv(k, "")
	}
	if err := os.WriteFile(config.ProjectConfigPath(root), []byte("MAX_ITERATIONS=7\n"), 0o644); err != nil {
		t.Fatalf("seed project config: %v", err)
	}
	writeUserConfig(t, "MAX_REPAIRS=5\n")

	ts := instancesServer(t, home)
	out, _ := getGlobalConfig(t, ts)

	if out.Repo != "" {
		t.Errorf("repo = %q, want empty — the global scope names no repo", out.Repo)
	}
	if strings.Join(out.Layers, ",") != "user" {
		t.Errorf("layers = %v, want [user]", out.Layers)
	}
	if len(out.Providers) == 0 {
		t.Errorf("providers should still be server-driven under the global scope")
	}

	repairs := mustKey(t, out.Keys, "MAX_REPAIRS")
	if repairs.Value != "5" || repairs.Layer != "user" {
		t.Errorf("MAX_REPAIRS = %q@%q, want 5@user", repairs.Value, repairs.Layer)
	}

	iter := mustKey(t, out.Keys, "MAX_ITERATIONS")
	if iter.Layer != "default" {
		t.Errorf("MAX_ITERATIONS layer = %q, want default — a repo's project layer must not leak in", iter.Layer)
	}
	if iter.Value == "7" {
		t.Errorf("MAX_ITERATIONS = 7, want the built-in default — the repo's .trau.ini leaked into the global view")
	}

	for _, v := range out.Keys {
		if v.Layer != "default" && v.Layer != "user" {
			t.Errorf("%s layer = %q, want default or user", v.Key, v.Layer)
		}
	}

	// The per-repo availability probe is a fact about a repo, and there is none.
	sync := mustKey(t, out.Keys, "TEAM_SYNC")
	if !sync.Editable || sync.DisabledReason != "" {
		t.Errorf("TEAM_SYNC = editable %v / %q, want editable with no repo-specific reason", sync.Editable, sync.DisabledReason)
	}
}

// TestGlobalConfigSecretMasking holds the credential rule at the global scope: a
// secret in ~/.trau.ini reports only that it is set and where from, and its value
// never crosses the wire.
func TestGlobalConfigSecretMasking(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("TRAU_LINEAR_API_KEY", "")
	writeUserConfig(t, "LINEAR_API_KEY=lin_api_supersecret\n")

	ts := instancesServer(t, home)
	out, raw := getGlobalConfig(t, ts)

	key := mustKey(t, out.Keys, "LINEAR_API_KEY")
	if !key.Secret || !key.Set {
		t.Errorf("LINEAR_API_KEY = secret %v / set %v, want both true", key.Secret, key.Set)
	}
	if key.Value != "" {
		t.Errorf("LINEAR_API_KEY value = %q, want it withheld", key.Value)
	}
	if key.Layer != "user" {
		t.Errorf("LINEAR_API_KEY layer = %q, want user", key.Layer)
	}
	if strings.Contains(raw, "lin_api_supersecret") {
		t.Errorf("response body leaked the credential: %s", raw)
	}
}

// TestGlobalConfigWriteUserLayer covers the global write path: the value lands in
// ~/.trau.ini, the repo's own file is untouched, and an unset takes the line back
// out so the built-in default applies again.
func TestGlobalConfigWriteUserLayer(t *testing.T) {
	home := t.TempDir()
	root := seedConfigRepo(t, home, "acme")
	t.Setenv("MAX_REPAIRS", "")
	t.Setenv("TRAU_MAX_REPAIRS", "")

	ts := instancesServer(t, home)
	res := putGlobalConfig(t, ts, ConfigWriteRequest{Key: "MAX_REPAIRS", Value: "4", Layer: "user"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", res.StatusCode)
	}
	var view ConfigKeyView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Value != "4" || view.Layer != "user" {
		t.Errorf("returned view = %q@%q, want 4@user", view.Value, view.Layer)
	}

	userHome, _ := os.UserHomeDir()
	userData, err := os.ReadFile(config.ProjectConfigPath(userHome))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userData), "MAX_REPAIRS=4") {
		t.Errorf("user config = %q, want it to hold MAX_REPAIRS=4", userData)
	}
	if _, err := os.Stat(config.ProjectConfigPath(root)); !os.IsNotExist(err) {
		t.Errorf("a global write must not touch a repo's project config, stat err = %v", err)
	}

	// A project's own view reads the same value back off the user layer.
	repoOut, _ := getConfig(t, ts, "acme")
	if seen := mustKey(t, repoOut.Keys, "MAX_REPAIRS"); seen.Value != "4" || seen.Layer != "user" {
		t.Errorf("project view MAX_REPAIRS = %q@%q, want 4@user", seen.Value, seen.Layer)
	}

	unset := putGlobalConfig(t, ts, ConfigWriteRequest{Key: "MAX_REPAIRS", Layer: "user", Unset: true})
	defer func() { _ = unset.Body.Close() }()
	if unset.StatusCode != http.StatusOK {
		t.Fatalf("unset status = %d, want 200", unset.StatusCode)
	}
	var back ConfigKeyView
	if err := json.NewDecoder(unset.Body).Decode(&back); err != nil {
		t.Fatalf("decode unset view: %v", err)
	}
	if back.Layer != "default" {
		t.Errorf("after unset MAX_REPAIRS layer = %q, want default", back.Layer)
	}
}

// TestGlobalConfigWriteSecret covers a credential written and cleared globally:
// the value reaches ~/.trau.ini but never comes back out over the wire.
func TestGlobalConfigWriteSecret(t *testing.T) {
	home := t.TempDir()
	seedConfigRepo(t, home, "acme")
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("TRAU_LINEAR_API_KEY", "")

	ts := instancesServer(t, home)
	res := putGlobalConfig(t, ts, ConfigWriteRequest{Key: "LINEAR_API_KEY", Value: "lin_api_written", Layer: "user"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "lin_api_written") {
		t.Errorf("write response echoed the credential: %s", body)
	}

	userHome, _ := os.UserHomeDir()
	userData, err := os.ReadFile(config.ProjectConfigPath(userHome))
	if err != nil {
		t.Fatalf("read user config: %v", err)
	}
	if !strings.Contains(string(userData), "LINEAR_API_KEY=lin_api_written") {
		t.Errorf("user config = %q, want it to hold the credential", userData)
	}

	unset := putGlobalConfig(t, ts, ConfigWriteRequest{Key: "LINEAR_API_KEY", Layer: "user", Unset: true})
	defer func() { _ = unset.Body.Close() }()
	if unset.StatusCode != http.StatusOK {
		t.Fatalf("unset status = %d, want 200", unset.StatusCode)
	}
	out, _ := getGlobalConfig(t, ts)
	if key := mustKey(t, out.Keys, "LINEAR_API_KEY"); key.Set {
		t.Errorf("LINEAR_API_KEY should read as unset after the unset")
	}
}

// TestGlobalConfigWriteRejections holds the guards on the global write path. The
// project layer is the notable one: there is no repo in scope, so a write aimed
// at one has nowhere to land and is refused rather than silently redirected.
func TestGlobalConfigWriteRejections(t *testing.T) {
	cases := []struct {
		name string
		req  ConfigWriteRequest
		want int
	}{
		{"project layer", ConfigWriteRequest{Key: "MAX_REPAIRS", Value: "3", Layer: "project"}, http.StatusBadRequest},
		{"local layer", ConfigWriteRequest{Key: "MAX_REPAIRS", Value: "3", Layer: "local"}, http.StatusBadRequest},
		{"empty layer", ConfigWriteRequest{Key: "MAX_REPAIRS", Value: "3"}, http.StatusBadRequest},
		{"unknown key", ConfigWriteRequest{Key: "NOT_A_KEY", Value: "x", Layer: "user"}, http.StatusBadRequest},
		{"read-only bin", ConfigWriteRequest{Key: "CLAUDE_BIN", Value: "/bin/sh", Layer: "user"}, http.StatusForbidden},
		{"read-only secret", ConfigWriteRequest{Key: "SERVE_TOKEN", Value: "sk-nope", Layer: "user"}, http.StatusForbidden},
		{"bad bool value", ConfigWriteRequest{Key: "NOTIFY", Value: "yes", Layer: "user"}, http.StatusBadRequest},
		{"non-int value", ConfigWriteRequest{Key: "MAX_ITERATIONS", Value: "lots", Layer: "user"}, http.StatusBadRequest},
		{"empty secret", ConfigWriteRequest{Key: "LINEAR_API_KEY", Value: "", Layer: "user"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			seedConfigRepo(t, home, "acme")
			ts := instancesServer(t, home)

			res := putGlobalConfig(t, ts, tc.req)
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
			userHome, _ := os.UserHomeDir()
			if _, err := os.Stat(config.ProjectConfigPath(userHome)); !os.IsNotExist(err) {
				t.Errorf("a refused write must not touch ~/.trau.ini, stat err = %v", err)
			}
		})
	}
}
