package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/registry"
)

// gitRepo makes a directory that looks like a git toplevel by planting a .git
// entry, matching how registration proves a repo. kind selects a .git directory
// (normal clone) or a .git file (worktree).
func gitRepo(t *testing.T, parent, name, kind string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	switch kind {
	case "file":
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatalf("write .git file: %v", err)
		}
	default:
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
	}
	return root
}

func TestRegisterRepoValidation(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	dirRepo := gitRepo(t, base, "acme", "dir")
	worktree := gitRepo(t, base, "wt", "file")
	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(base, "afile")
	if err := os.WriteFile(regularFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ts := controlServer(t, home, nil)

	cases := []struct {
		name   string
		path   string
		status int
	}{
		{"git dir toplevel", dirRepo, http.StatusCreated},
		{"git file worktree", worktree, http.StatusCreated},
		{"missing path", "", http.StatusBadRequest},
		{"relative path", "relative/acme", http.StatusBadRequest},
		{"nonexistent", filepath.Join(base, "nope"), http.StatusBadRequest},
		{"not a directory", regularFile, http.StatusBadRequest},
		{"not a git toplevel", plain, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: tc.path})
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.status)
			}
			if tc.status == http.StatusBadRequest {
				var body map[string]string
				if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
					t.Fatalf("decode error body: %v", err)
				}
				if body["error"] == "" {
					t.Errorf("expected a clear error message, got %v", body)
				}
			}
		})
	}
}

func TestRegisterThenStartWithoutRestart(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: "acme"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-register start = %d, want 403", res.StatusCode)
	}

	res = postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace.json")); !os.IsNotExist(err) {
		t.Errorf("registration created a legacy workspace.json; storage must be the hub database")
	}
	if roots, _ := testRegistrationsAt(t, home).Registered(); !slices.Contains(roots, repo) {
		t.Errorf("registration not persisted to the hub database")
	}
	if _, err := os.Stat(filepath.Join(home, ".trau.ini")); !os.IsNotExist(err) {
		t.Errorf("registration must not touch .trau.ini")
	}

	if allowed := allowedRepoNames(t, ts); !allowed["acme"] {
		t.Errorf("registered repo not reported allowed in repos list")
	}

	res = postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: "acme"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("post-register start = %d, want 202", res.StatusCode)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(fake.spawns))
	}
	if fake.spawns[0].Dir != repo {
		t.Errorf("spawn Dir = %q, want %q", fake.spawns[0].Dir, repo)
	}
}

func TestEffectiveAllowlistMergesSeedAndRegistered(t *testing.T) {
	home := t.TempDir()
	seed := gitRepo(t, t.TempDir(), "seeded", "dir")
	added := gitRepo(t, t.TempDir(), "added", "dir")
	_, ts := controlServer(t, home, []string{seed})

	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: added})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}

	allowed := allowedRepoNames(t, ts)
	if !allowed["seeded"] {
		t.Errorf("SERVE_WORKSPACE seed repo not allowed: %v", allowed)
	}
	if !allowed["added"] {
		t.Errorf("registered repo not allowed: %v", allowed)
	}
}

func TestRegistrationPersistsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")

	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}

	fake, ts2 := controlServer(t, home, nil)
	res = postJSON(t, ts2.URL+APIPrefix+"/instances", StartRequest{Repo: "acme"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("post-restart start = %d, want 202", res.StatusCode)
	}
	if len(fake.spawns) != 1 {
		t.Fatalf("post-restart spawns = %d, want 1", len(fake.spawns))
	}
}

// authReq issues method against url with an optional bearer token and JSON body,
// reading the whole response so it can assert on both status and message.
func authReq(t *testing.T, method, url, token string, body any) (*http.Response, string) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	data, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("read %s body: %v", url, err)
	}
	return res, string(data)
}

// TestRegistrationExposureGate covers the bind × token × SERVE_ALLOW_REGISTER
// matrix for both register and unregister: loopback is always open, an exposed
// bind needs the key on top of a valid token, and the token gate still runs first.
func TestRegistrationExposureGate(t *testing.T) {
	cases := []struct {
		name           string
		bind           string
		serverToken    string
		reqToken       string
		allowRegister  bool
		wantRegister   int
		wantUnregister int
		namesKey       bool
	}{
		{"loopback open without key", "127.0.0.1", "", "", false, http.StatusCreated, http.StatusOK, false},
		{"loopback ignores key", "127.0.0.1", "", "", true, http.StatusCreated, http.StatusOK, false},
		{"exposed token, key off, refused", "0.0.0.0", "s3cret", "s3cret", false, http.StatusForbidden, http.StatusForbidden, true},
		{"exposed token, key on, allowed", "0.0.0.0", "s3cret", "s3cret", true, http.StatusCreated, http.StatusOK, false},
		{"exposed missing token, key off", "0.0.0.0", "s3cret", "", false, http.StatusUnauthorized, http.StatusUnauthorized, false},
		{"exposed missing token, key on", "0.0.0.0", "s3cret", "", true, http.StatusUnauthorized, http.StatusUnauthorized, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			base := t.TempDir()
			toRegister := gitRepo(t, base, "toregister", "dir")
			toUnregister := gitRepo(t, base, "tounregister", "dir")
			store := testRegistrationsAt(t, home)
			if err := store.Register(toUnregister); err != nil {
				t.Fatalf("seed unregister target: %v", err)
			}

			s := New("1.2.3", tc.bind, tc.serverToken, nil, tc.allowRegister, store)
			s.home = home
			s.sup = &fakeSupervisor{}
			ts := httptest.NewServer(s.Handler())
			t.Cleanup(ts.Close)

			res, body := authReq(t, http.MethodPost, ts.URL+APIPrefix+"/repos", tc.reqToken, RegisterRepoRequest{Path: toRegister})
			if res.StatusCode != tc.wantRegister {
				t.Fatalf("register = %d, want %d (%s)", res.StatusCode, tc.wantRegister, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("register refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
			registered, _ := store.Registered()
			if got := slices.Contains(registered, toRegister); got != (tc.wantRegister == http.StatusCreated) {
				t.Errorf("registered(%s) = %v after register status %d", toRegister, got, tc.wantRegister)
			}

			res, body = authReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/tounregister", tc.reqToken, nil)
			if res.StatusCode != tc.wantUnregister {
				t.Fatalf("unregister = %d, want %d (%s)", res.StatusCode, tc.wantUnregister, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("unregister refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
			registered, _ = store.Registered()
			stillRegistered := slices.Contains(registered, toUnregister)
			if wantStill := tc.wantUnregister != http.StatusOK; stillRegistered != wantStill {
				t.Errorf("registered(%s) = %v after unregister status %d", toUnregister, stillRegistered, tc.wantUnregister)
			}
		})
	}
}

func TestUnregisterRepo(t *testing.T) {
	base := t.TempDir()
	registered := gitRepo(t, base, "registered", "dir")
	seeded := gitRepo(t, base, "seeded", "dir")

	cases := []struct {
		name       string
		target     string
		wantStatus int
		errSubstr  string
		verify     func(t *testing.T, home string, ts *httptest.Server)
	}{
		{
			name:       "web-registered repo drops to observe-only",
			target:     "registered",
			wantStatus: http.StatusOK,
			verify: func(t *testing.T, home string, ts *httptest.Server) {
				if allowedRepoNames(t, ts)["registered"] {
					t.Error("unregistered repo still reported allowed")
				}
				res := postJSON(t, ts.URL+APIPrefix+"/instances", StartRequest{Repo: "registered"})
				_ = res.Body.Close()
				if res.StatusCode != http.StatusForbidden {
					t.Errorf("post-unregister start = %d, want 403", res.StatusCode)
				}
				if roots, _ := testRegistrationsAt(t, home).Registered(); len(roots) != 0 {
					t.Errorf("registration store still lists %v", roots)
				}
				if _, err := os.Stat(filepath.Join(registered, ".git")); err != nil {
					t.Errorf("repo on disk was touched: %v", err)
				}
			},
		},
		{
			name:       "config-owned seed repo is refused",
			target:     "seeded",
			wantStatus: http.StatusConflict,
			errSubstr:  "SERVE_WORKSPACE",
			verify: func(t *testing.T, _ string, ts *httptest.Server) {
				if !allowedRepoNames(t, ts)["seeded"] {
					t.Error("refused seed repo lost its allowlist entry")
				}
			},
		},
		{
			name:       "unknown repo is not found",
			target:     "ghost",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			_, ts := controlServer(t, home, []string{seeded})
			res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: registered})
			_ = res.Body.Close()
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("register precondition = %d, want 201", res.StatusCode)
			}

			res, body := deleteReq(t, ts, APIPrefix+"/repos/"+tc.target)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("DELETE %s = %d, want %d (%s)", tc.target, res.StatusCode, tc.wantStatus, body)
			}
			if tc.errSubstr != "" && !strings.Contains(body, tc.errSubstr) {
				t.Errorf("error %q does not name %q", body, tc.errSubstr)
			}
			if tc.verify != nil {
				tc.verify(t, home, ts)
			}
		})
	}
}

func TestUnregisterPersistsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")

	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	res, body := deleteReq(t, ts, APIPrefix+"/repos/acme")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unregister = %d, want 200 (%s)", res.StatusCode, body)
	}

	fake, ts2 := controlServer(t, home, nil)
	res = postJSON(t, ts2.URL+APIPrefix+"/instances", StartRequest{Repo: "acme"})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("post-restart start = %d, want 403", res.StatusCode)
	}
	if len(fake.spawns) != 0 {
		t.Fatalf("post-restart spawns = %d, want 0", len(fake.spawns))
	}
}

func TestUnregisterKeepsRepoBrowsable(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	if err := testRegistrationsAt(t, home).Remember([]registry.Repo{{Name: filepath.Base(repo), Root: repo, RunsDir: filepath.Join(repo, ".trau", "runs")}}); err != nil {
		t.Fatalf("seed known repo: %v", err)
	}

	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/acme")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unregister = %d, want 200 (%s)", res.StatusCode, body)
	}

	runsRes, _ := get(t, ts, APIPrefix+"/repos/acme/runs")
	if runsRes.StatusCode != http.StatusOK {
		t.Errorf("runs no longer browsable after unregister: %d", runsRes.StatusCode)
	}
	if allowedRepoNames(t, ts)["acme"] {
		t.Error("repo still allowed after unregister, want observe-only")
	}
}

func allowedRepoNames(t *testing.T, ts *httptest.Server) map[string]bool {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET repos = %d, want 200", res.StatusCode)
	}
	var resp ReposResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	allowed := make(map[string]bool, len(resp.Repos))
	for _, rv := range resp.Repos {
		if rv.Allowed {
			allowed[rv.Name] = true
		}
	}
	return allowed
}
