package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	folder := filepath.Join(base, "services")
	gitRepo(t, folder, "api-companies", "dir")

	_, ts := controlServer(t, home, nil)

	cases := []struct {
		name   string
		path   string
		status int
	}{
		{"git dir toplevel", dirRepo, http.StatusCreated},
		{"git file worktree", worktree, http.StatusCreated},
		{"folder of repos", folder, http.StatusCreated},
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

// TestGitignoreIsANoOpForAFolderRepo holds the essentials toggle to ADR 0030: a
// folder is not a git repository, so a .gitignore written into one is clutter
// nothing reads, and the endpoint says plainly that it wrote none.
func TestGitignoreIsANoOpForAFolderRepo(t *testing.T) {
	home := t.TempDir()
	folder := filepath.Join(t.TempDir(), "services")
	gitRepo(t, folder, "api", "dir")
	_, ts := controlServer(t, home, []string{folder})

	res := postJSON(t, ts.URL+APIPrefix+"/repos/services/gitignore", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["gitignored"] != false {
		t.Errorf("gitignored = %v, want false", body["gitignored"])
	}
	if _, err := os.Stat(filepath.Join(folder, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("stat .gitignore = %v, want it never written", err)
	}
}

func TestRegisterThenDryRunWithoutRestart(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	fake, ts := controlServer(t, home, nil)

	res := postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("pre-register dry-run = %d, want 403", res.StatusCode)
	}

	res = postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace.json")); !os.IsNotExist(err) {
		t.Errorf("registration created a legacy workspace.json; storage must be the hub database")
	}
	if roots, _ := testStoresAt(t, home).Registrations().Registered(); !slices.Contains(roots, repo) {
		t.Errorf("registration not persisted to the hub database")
	}
	if _, err := os.Stat(filepath.Join(home, ".trau.ini")); !os.IsNotExist(err) {
		t.Errorf("registration must not touch .trau.ini")
	}

	if allowed := allowedRepoNames(t, ts); !allowed["acme"] {
		t.Errorf("registered repo not reported allowed in repos list")
	}

	res = postJSON(t, ts.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("post-register dry-run = %d, want 200", res.StatusCode)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("captures = %d, want 1", len(fake.captures))
	}
	if fake.captures[0].Dir != repo {
		t.Errorf("capture Dir = %q, want %q", fake.captures[0].Dir, repo)
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
	res = postJSON(t, ts2.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("post-restart dry-run = %d, want 200", res.StatusCode)
	}
	if len(fake.captures) != 1 {
		t.Fatalf("post-restart captures = %d, want 1", len(fake.captures))
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
// matrix for every write to the repo set — register, unregister, and remove:
// loopback is always open, an exposed bind needs the key on top of a valid token,
// and the token gate still runs first.
func TestRegistrationExposureGate(t *testing.T) {
	cases := []struct {
		name          string
		bind          string
		serverToken   string
		reqToken      string
		allowRegister bool
		wantRegister  int
		wantDelete    int
		namesKey      bool
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
			t.Setenv("HOME", home)
			base := t.TempDir()
			toRegister := gitRepo(t, base, "toregister", "dir")
			toUnregister := gitRepo(t, base, "tounregister", "dir")
			toForget := gitRepo(t, base, "toforget", "dir")
			store := testStoresAt(t, home)
			if err := store.Registrations().Register(toUnregister); err != nil {
				t.Fatalf("seed unregister target: %v", err)
			}
			if err := store.Registrations().Remember([]registry.Repo{workspaceRepo(toForget)}); err != nil {
				t.Fatalf("seed forget target: %v", err)
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
			registered, _ := store.Registrations().Registered()
			if got := slices.Contains(registered, toRegister); got != (tc.wantRegister == http.StatusCreated) {
				t.Errorf("registered(%s) = %v after register status %d", toRegister, got, tc.wantRegister)
			}

			res, body = authReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/tounregister", tc.reqToken, nil)
			if res.StatusCode != tc.wantDelete {
				t.Fatalf("unregister = %d, want %d (%s)", res.StatusCode, tc.wantDelete, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("unregister refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
			registered, _ = store.Registrations().Registered()
			stillRegistered := slices.Contains(registered, toUnregister)
			if wantStill := tc.wantDelete != http.StatusOK; stillRegistered != wantStill {
				t.Errorf("registered(%s) = %v after unregister status %d", toUnregister, stillRegistered, tc.wantDelete)
			}

			res, body = authReq(t, http.MethodDelete, ts.URL+APIPrefix+"/repos/toforget?forget=1", tc.reqToken, nil)
			if res.StatusCode != tc.wantDelete {
				t.Fatalf("forget = %d, want %d (%s)", res.StatusCode, tc.wantDelete, body)
			}
			if tc.namesKey && !strings.Contains(body, "SERVE_ALLOW_REGISTER") {
				t.Errorf("forget refusal %q does not name SERVE_ALLOW_REGISTER", body)
			}
			known, _ := store.Registrations().Known()
			stillKnown := slices.ContainsFunc(known, func(repo registry.Repo) bool { return repo.Root == toForget })
			if wantStill := tc.wantDelete != http.StatusOK; stillKnown != wantStill {
				t.Errorf("known(%s) = %v after forget status %d", toForget, stillKnown, tc.wantDelete)
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
				res := postJSON(t, ts.URL+APIPrefix+"/repos/registered/dry-run", nil)
				_ = res.Body.Close()
				if res.StatusCode != http.StatusForbidden {
					t.Errorf("post-unregister dry-run = %d, want 403", res.StatusCode)
				}
				if roots, _ := testStoresAt(t, home).Registrations().Registered(); len(roots) != 0 {
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
	res = postJSON(t, ts2.URL+APIPrefix+"/repos/acme/dry-run", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("post-restart dry-run = %d, want 403", res.StatusCode)
	}
	if len(fake.captures) != 0 {
		t.Fatalf("post-restart captures = %d, want 0", len(fake.captures))
	}
}

func TestUnregisterKeepsRepoBrowsable(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{{Name: filepath.Base(repo), Root: repo, RunsDir: filepath.Join(repo, ".trau", "runs")}}); err != nil {
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

// TestUnregisterKeepsUnrunRepoListed covers the row a registration alone put on
// the list: dropping it to observe-only before its first loop ran must leave it
// listed, since removal is the only control that takes a project off the card.
func TestUnregisterKeepsUnrunRepoListed(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, t.TempDir(), "acme", "dir")

	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: repo})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/"+url.PathEscape(repo))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unregister = %d, want 200 (%s)", res.StatusCode, body)
	}

	if roots := listedRepoRoots(t, ts); !slices.Contains(roots, repo) {
		t.Fatalf("repo left the list after unregister: %v", roots)
	}
	if allowedRepoNames(t, ts)["acme"] {
		t.Error("repo still allowed after unregister, want observe-only")
	}
}

func listRepos(t *testing.T, ts *httptest.Server) []RepoView {
	t.Helper()
	res, body := get(t, ts, APIPrefix+"/repos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET repos = %d, want 200", res.StatusCode)
	}
	var resp ReposResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode repos: %v", err)
	}
	return resp.Repos
}

func TestForgetRepo(t *testing.T) {
	base := t.TempDir()
	registered := gitRepo(t, base, "registered", "dir")
	observed := gitRepo(t, base, "observed", "dir")
	seeded := gitRepo(t, base, "seeded", "dir")
	running := gitRepo(t, base, "running", "dir")

	cases := []struct {
		name       string
		target     string
		wantStatus int
		errSubstr  string
		verify     func(t *testing.T, home string, ts *httptest.Server)
	}{
		{
			name:       "web-registered repo goes with its registration",
			target:     "registered",
			wantStatus: http.StatusOK,
			verify: func(t *testing.T, home string, ts *httptest.Server) {
				if slices.Contains(listedRepoRoots(t, ts), registered) {
					t.Error("removed repo is still listed")
				}
				if roots, _ := testStoresAt(t, home).Registrations().Registered(); len(roots) != 0 {
					t.Errorf("registration store still lists %v", roots)
				}
				if _, err := os.Stat(filepath.Join(registered, ".git")); err != nil {
					t.Errorf("repo on disk was touched: %v", err)
				}
			},
		},
		{
			name:       "observe-only repo the hub only watched leaves the list",
			target:     "observed",
			wantStatus: http.StatusOK,
			verify: func(t *testing.T, home string, ts *httptest.Server) {
				if slices.Contains(listedRepoRoots(t, ts), observed) {
					t.Error("removed repo is still listed")
				}
				known, _ := testStoresAt(t, home).Registrations().Known()
				for _, repo := range known {
					if repo.Root == observed {
						t.Error("known set still holds the removed repo")
					}
				}
			},
		},
		{
			name:       "config-owned seed repo is refused",
			target:     "seeded",
			wantStatus: http.StatusConflict,
			errSubstr:  "SERVE_WORKSPACE",
			verify: func(t *testing.T, _ string, ts *httptest.Server) {
				if !slices.Contains(listedRepoRoots(t, ts), seeded) {
					t.Error("refused seed repo left the list")
				}
			},
		},
		{
			name:       "repo with a live loop is refused",
			target:     "running",
			wantStatus: http.StatusConflict,
			errSubstr:  "stop it",
			verify: func(t *testing.T, _ string, ts *httptest.Server) {
				if !slices.Contains(listedRepoRoots(t, ts), running) {
					t.Error("refused live repo left the list")
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
			if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{
				workspaceRepo(observed),
				workspaceRepo(running),
			}); err != nil {
				t.Fatalf("seed known repos: %v", err)
			}
			writeEntry(t, home, registry.Entry{
				PID:          os.Getpid(),
				RepoRoot:     running,
				SessionState: registry.StateIdle,
				StartedAt:    time.Now(),
				Heartbeat:    time.Now(),
			})
			_, ts := controlServer(t, home, []string{seeded})
			res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: registered})
			_ = res.Body.Close()
			if res.StatusCode != http.StatusCreated {
				t.Fatalf("register precondition = %d, want 201", res.StatusCode)
			}

			res, body := deleteReq(t, ts, APIPrefix+"/repos/"+tc.target+"?forget=1")
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

// TestForgetByRootRemovesTheRightNamesake covers the case that made the list
// unclearable: two scratch clones known by the same basename, where only the root
// identifies which row the user pressed Remove on. The shared name identifies
// neither, so it is refused as ambiguous — naming the fix — rather than resolved
// to whichever came first or reported as a repo the hub never heard of.
func TestForgetByRootRemovesTheRightNamesake(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	first := gitRepo(t, filepath.Join(base, "one"), "repo", "dir")
	second := gitRepo(t, filepath.Join(base, "two"), "repo", "dir")
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{
		workspaceRepo(first),
		workspaceRepo(second),
	}); err != nil {
		t.Fatalf("seed known repos: %v", err)
	}

	_, ts := controlServer(t, home, nil)
	res, body := deleteReq(t, ts, APIPrefix+"/repos/repo?forget=1")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE by ambiguous name = %d, want 409 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, "more than one repo") {
		t.Errorf("ambiguity refusal %q does not say the name is shared", body)
	}
	if roots := listedRepoRoots(t, ts); len(roots) != 2 {
		t.Fatalf("ambiguous removal took a namesake: %v", roots)
	}

	res, body = deleteReq(t, ts, APIPrefix+"/repos/"+url.PathEscape(second)+"?forget=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE by root = %d, want 200 (%s)", res.StatusCode, body)
	}

	roots := listedRepoRoots(t, ts)
	if slices.Contains(roots, second) {
		t.Errorf("addressed repo is still listed: %v", roots)
	}
	if !slices.Contains(roots, first) {
		t.Errorf("namesake was removed too: %v", roots)
	}
}

// TestForgetRepoWithANonCanonicalStoredRoot covers the row the hub served forever:
// a known-repos root recorded as the loop resolved it rather than cleaned, where
// the removal reported success over a DELETE that matched nothing.
func TestForgetRepoWithANonCanonicalStoredRoot(t *testing.T) {
	cases := []struct {
		name  string
		spell func(root string) string
	}{
		{
			name:  "trailing separator",
			spell: func(root string) string { return root + string(filepath.Separator) },
		},
		{
			name:  "forward slashes as git reports them on windows",
			spell: filepath.ToSlash,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stored := tc.spell(gitRepo(t, t.TempDir(), "observed", "dir"))
			if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{workspaceRepo(stored)}); err != nil {
				t.Fatalf("seed known repo: %v", err)
			}

			_, ts := controlServer(t, home, nil)
			res, body := deleteReq(t, ts, APIPrefix+"/repos/"+url.PathEscape(stored)+"?forget=1")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("DELETE by stored root = %d, want 200 (%s)", res.StatusCode, body)
			}
			if roots := listedRepoRoots(t, ts); len(roots) != 0 {
				t.Errorf("removed repo is still listed: %v", roots)
			}
			if known, _ := testStoresAt(t, home).Registrations().Known(); len(known) != 0 {
				t.Errorf("known set still holds the removed repo: %v", known)
			}
		})
	}
}

// TestUnregisterRepoWithANonCanonicalRegisteredRoot covers Make observe-only on a
// registration a legacy import left uncleaned, which used to 404 on a byte compare
// against the stored root.
func TestUnregisterRepoWithANonCanonicalRegisteredRoot(t *testing.T) {
	home := t.TempDir()
	stored := gitRepo(t, t.TempDir(), "acme", "dir") + string(filepath.Separator)
	if err := testStoresAt(t, home).Registrations().Register(stored); err != nil {
		t.Fatalf("seed registration: %v", err)
	}

	_, ts := controlServer(t, home, nil)
	res, body := deleteReq(t, ts, APIPrefix+"/repos/"+url.PathEscape(stored))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unregister = %d, want 200 (%s)", res.StatusCode, body)
	}
	if roots, _ := testStoresAt(t, home).Registrations().Registered(); len(roots) != 0 {
		t.Errorf("registration store still lists %v", roots)
	}
	if allowedRepoNames(t, ts)["acme"] {
		t.Error("repo still allowed after unregister, want observe-only")
	}
}

// TestTwoSpellingsOfOneRepoListAsOneRow covers the duplicate the picker served:
// the web registers a directory with separators while the loop running in it
// heartbeats the spelling `git rev-parse` hands back, and the list showed both
// with the flags split between them.
func TestTwoSpellingsOfOneRepoListAsOneRow(t *testing.T) {
	home := t.TempDir()
	root := gitRepo(t, t.TempDir(), "acme", "dir")

	_, ts := controlServer(t, home, nil)
	res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root, Sync: new(bool)})
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, want 201", res.StatusCode)
	}
	writeEntry(t, home, registry.Entry{
		PID:      os.Getpid(),
		RepoRoot: filepath.ToSlash(root) + "/",
		RunsDir:  filepath.Join(root, ".trau", "runs"),
	})

	repos := listRepos(t, ts)
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want one row for %s", repos, root)
	}
	if got := repos[0]; got.Root != root || !got.Live || !got.Registered || !got.Allowed {
		t.Errorf("row = %+v, want %s live, registered and allowed", got, root)
	}
}

// TestRepoListOrderIsStableAcrossReads pins the ordering the 5s poll depends on:
// two repos sharing a name used to fall out of a map in whichever order the
// runtime chose, so a name-addressed lookup resolved to a different root between
// polls and the board appeared to rewind.
func TestRepoListOrderIsStableAcrossReads(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	if err := testStoresAt(t, home).Registrations().Remember([]registry.Repo{
		workspaceRepo(gitRepo(t, filepath.Join(base, "one"), "repo", "dir")),
		workspaceRepo(gitRepo(t, filepath.Join(base, "two"), "repo", "dir")),
	}); err != nil {
		t.Fatalf("seed known repos: %v", err)
	}

	_, ts := controlServer(t, home, nil)
	first := listedRepoRoots(t, ts)
	for i := range 5 {
		if got := listedRepoRoots(t, ts); !slices.Equal(got, first) {
			t.Fatalf("read %d listed %v, want the order of the first read %v", i, got, first)
		}
	}
}

// TestUnregisterResolvesByRootAndRefusesAmbiguity covers the removal that could
// not reach its row: unregister searched the registered roots alone under a bare
// name, so the only way to address one of two namesakes 404'd as "not registered".
func TestUnregisterResolvesByRootAndRefusesAmbiguity(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	first := gitRepo(t, filepath.Join(base, "one"), "repo", "dir")
	second := gitRepo(t, filepath.Join(base, "two"), "repo", "dir")

	_, ts := controlServer(t, home, nil)
	for _, root := range []string{first, second} {
		res := postJSON(t, ts.URL+APIPrefix+"/repos", RegisterRepoRequest{Path: root, Sync: new(bool)})
		_ = res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("register %s = %d, want 201", root, res.StatusCode)
		}
	}

	res, body := deleteReq(t, ts, APIPrefix+"/repos/repo")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE by ambiguous name = %d, want 409 (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, "more than one repo") {
		t.Errorf("ambiguity refusal %q does not say the name is shared", body)
	}

	res, body = deleteReq(t, ts, APIPrefix+"/repos/"+url.PathEscape(second))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("DELETE by root = %d, want 200 (%s)", res.StatusCode, body)
	}
	registered, _ := testStoresAt(t, home).Registrations().Registered()
	if !slices.Equal(registered, []string{first}) {
		t.Errorf("registered = %v, want only %s", registered, first)
	}
}

func allowedRepoNames(t *testing.T, ts *httptest.Server) map[string]bool {
	t.Helper()
	repos := listRepos(t, ts)
	allowed := make(map[string]bool, len(repos))
	for _, rv := range repos {
		if rv.Allowed {
			allowed[rv.Name] = true
		}
	}
	return allowed
}

func listedRepoRoots(t *testing.T, ts *httptest.Server) []string {
	t.Helper()
	repos := listRepos(t, ts)
	roots := make([]string, 0, len(repos))
	for _, rv := range repos {
		roots = append(roots, rv.Root)
	}
	return roots
}
