package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func discover(t *testing.T, ts *httptest.Server, path string) (*http.Response, FSDiscoverResponse) {
	t.Helper()
	target := ts.URL + APIPrefix + "/fs/discover"
	if path != "" {
		target += "?path=" + url.QueryEscape(path)
	}
	res, err := http.Get(target)
	if err != nil {
		t.Fatalf("GET discover: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body FSDiscoverResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode discover: %v", err)
		}
	}
	return res, body
}

func childNames(entries []FSEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, filepath.ToSlash(e.Name))
	}
	slices.Sort(names)
	return names
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func TestDiscoverOffersChildRepos(t *testing.T) {
	home := t.TempDir()
	parent := mkdir(t, filepath.Join(home, "acme"))
	gitRepo(t, parent, "api", "dir")
	gitRepo(t, mkdir(t, filepath.Join(parent, "services")), "web", "file")
	mkdir(t, filepath.Join(parent, "notes"))
	ts := browseServer(t, home)

	res, body := discover(t, ts, parent)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if body.IsRepo {
		t.Fatal("a folder without .git reports is_repo = true")
	}
	if body.Name != "acme" {
		t.Fatalf("name = %q, want %q", body.Name, "acme")
	}
	want := []string{"api", "services/web"}
	if got := childNames(body.Children); !slices.Equal(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
}

// A picked repository is offered as itself and never scanned below, so a repo
// that happens to hold another one can never be onboarded as a nest of two.
func TestDiscoverNeverScansBelowARepo(t *testing.T) {
	home := t.TempDir()
	outer := gitRepo(t, home, "acme", "dir")
	gitRepo(t, outer, "vendored", "dir")
	ts := browseServer(t, home)

	_, body := discover(t, ts, outer)
	if !body.IsRepo {
		t.Fatalf("discovering %q reports is_repo = false", outer)
	}
	if len(body.Children) != 0 {
		t.Fatalf("children = %v, want none", childNames(body.Children))
	}
}

func TestDiscoverStopsAtTwoLevels(t *testing.T) {
	home := t.TempDir()
	parent := mkdir(t, filepath.Join(home, "work"))
	deep := mkdir(t, filepath.Join(parent, "one", "two"))
	gitRepo(t, deep, "buried", "dir")
	ts := browseServer(t, home)

	_, body := discover(t, ts, parent)
	if len(body.Children) != 0 {
		t.Fatalf("children = %v, want none three levels down", childNames(body.Children))
	}
}

func TestDiscoverSkipsUnreadableDirs(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not gate reads here")
	}
	home := t.TempDir()
	parent := mkdir(t, filepath.Join(home, "acme"))
	gitRepo(t, parent, "api", "dir")
	locked := mkdir(t, filepath.Join(parent, "locked"))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ts := browseServer(t, home)

	res, body := discover(t, ts, parent)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := childNames(body.Children); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("children = %v, want [api]", got)
	}
}

// A symlinked child is left alone: following one would let the scan walk out of
// the picked tree, and a link pointing back up would loop forever.
func TestDiscoverIgnoresSymlinkedChildren(t *testing.T) {
	home := t.TempDir()
	parent := mkdir(t, filepath.Join(home, "acme"))
	gitRepo(t, parent, "api", "dir")
	elsewhere := gitRepo(t, home, "outside", "dir")
	if err := os.Symlink(elsewhere, filepath.Join(parent, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(parent, filepath.Join(parent, "loop")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ts := browseServer(t, home)

	_, body := discover(t, ts, parent)
	if got := childNames(body.Children); !slices.Equal(got, []string{"api"}) {
		t.Fatalf("children = %v, want [api]", got)
	}
}

func TestDiscoverRejectsUnusablePaths(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "readme.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := browseServer(t, home)

	cases := []struct {
		name string
		path string
	}{
		{"missing", ""},
		{"relative", filepath.Join("Projects", "acme")},
		{"nonexistent", filepath.Join(home, "nope")},
		{"a file", file},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := discover(t, ts, tc.path)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestDiscoverRefusedOnExposedBind(t *testing.T) {
	ts := exposedServer(t, "0.0.0.0", "secret")

	if got := statusWithToken(t, ts, APIPrefix+"/fs/discover", "secret"); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}
}

func TestInitCreatesRepoWithBaseBranch(t *testing.T) {
	home := t.TempDir()
	empty := mkdir(t, filepath.Join(home, "fresh"))
	ts := browseServer(t, home)

	res := postJSON(t, ts.URL+APIPrefix+"/fs/init", GitInitRequest{Path: empty})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var body GitInitResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode init: %v", err)
	}
	if body.Path != empty {
		t.Fatalf("path = %q, want %q", body.Path, empty)
	}
	if body.Branch == "" {
		t.Fatal("init reports no base branch")
	}
	if _, err := validateRepoPath(empty); err != nil {
		t.Fatalf("initialized folder is not registrable: %v", err)
	}
	if _, found := discover(t, ts, empty); !found.IsRepo {
		t.Fatal("initialized folder does not discover as a repo")
	}
}

func TestInitRefusesFoldersGitAlreadyCovers(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, home, "acme", "dir")
	parent := mkdir(t, filepath.Join(home, "group"))
	gitRepo(t, parent, "api", "dir")
	gitRepo(t, parent, "web", "dir")
	ts := browseServer(t, home)

	cases := []struct {
		name string
		path string
		want string
	}{
		{"already a repo", repo, "already a git repository"},
		{"holds child repos", parent, "instead of nesting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postJSON(t, ts.URL+APIPrefix+"/fs/init", GitInitRequest{Path: tc.path})
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", res.StatusCode)
			}
			var body map[string]string
			if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
				t.Fatalf("decode refusal: %v", err)
			}
			if !strings.Contains(body["error"], tc.want) {
				t.Fatalf("refusal = %q, want it to mention %q", body["error"], tc.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(parent, ".git")); !os.IsNotExist(err) {
		t.Fatal("a refused init left a .git behind")
	}
}

func TestInitRejectsUnusableRequests(t *testing.T) {
	home := t.TempDir()
	ts := browseServer(t, home)

	res, err := http.Get(ts.URL + APIPrefix + "/fs/init")
	if err != nil {
		t.Fatalf("GET init: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}

	bad := postJSON(t, ts.URL+APIPrefix+"/fs/init", GitInitRequest{Path: filepath.Join(home, "nope")})
	defer func() { _ = bad.Body.Close() }()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", bad.StatusCode)
	}
}

// A bearer token alone must not let an exposed hub write a repository onto the
// host, so init is refused the same way registration is.
func TestInitRefusedOnExposedBind(t *testing.T) {
	ts := exposedServer(t, "0.0.0.0", "secret")

	buf, err := json.Marshal(GitInitRequest{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+APIPrefix+"/fs/init", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST init: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if !strings.Contains(body["error"], "SERVE_ALLOW_REGISTER") {
		t.Fatalf("refusal = %q, want it to name SERVE_ALLOW_REGISTER", body["error"])
	}
}
