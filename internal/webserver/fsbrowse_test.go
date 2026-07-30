package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// browseServer is a loopback hub whose home directory is a directory the test
// owns, so the roots listing is assertable. USERPROFILE is set alongside HOME
// because that is where os.UserHomeDir reads it from on Windows.
func browseServer(t *testing.T, home string) *httptest.Server {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	ts := httptest.NewServer(New("1.2.3", "127.0.0.1", "", nil, false, testStores(t)).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func browse(t *testing.T, ts *httptest.Server, path string) (*http.Response, FSBrowseResponse) {
	t.Helper()
	target := ts.URL + APIPrefix + "/fs/browse"
	if path != "" {
		target += "?path=" + url.QueryEscape(path)
	}
	res, err := http.Get(target)
	if err != nil {
		t.Fatalf("GET browse: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body FSBrowseResponse
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode browse: %v", err)
		}
	}
	return res, body
}

func entryNames(entries []FSEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

func TestBrowseRootsListsHome(t *testing.T) {
	home := t.TempDir()
	ts := browseServer(t, home)

	res, body := browse(t, ts, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if body.Path != "" || body.Parent != "" {
		t.Fatalf("roots listing carries path %q parent %q, want both empty", body.Path, body.Parent)
	}
	if body.Separator != string(filepath.Separator) {
		t.Fatalf("separator = %q, want %q", body.Separator, string(filepath.Separator))
	}
	if len(body.Entries) == 0 || body.Entries[0].Path != home {
		t.Fatalf("entries = %v, want home %q first", body.Entries, home)
	}
	for _, e := range body.Entries {
		if !filepath.IsAbs(e.Path) {
			t.Fatalf("root %q is not absolute", e.Path)
		}
	}
}

func TestBrowseListsDirectoriesOnly(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "Projects")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	clone := gitRepo(t, base, "acme", "dir")
	worktree := gitRepo(t, base, "acme-wt", "file")
	plain := filepath.Join(base, "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := browseServer(t, home)

	res, body := browse(t, ts, base)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if body.Path != base {
		t.Fatalf("path = %q, want %q", body.Path, base)
	}
	if body.Parent != home {
		t.Fatalf("parent = %q, want %q", body.Parent, home)
	}
	if body.IsRepo {
		t.Fatal("browsing a plain directory reports it as a repo")
	}
	want := []string{"acme", "acme-wt", "notes"}
	if got := entryNames(body.Entries); !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	repos := map[string]bool{clone: true, worktree: true, plain: false}
	for _, e := range body.Entries {
		if e.IsRepo != repos[e.Path] {
			t.Fatalf("entry %q is_repo = %v, want %v", e.Path, e.IsRepo, repos[e.Path])
		}
	}
}

func TestBrowseReportsCurrentRepoAndVolumeRootParent(t *testing.T) {
	home := t.TempDir()
	clone := gitRepo(t, home, "acme", "dir")
	ts := browseServer(t, home)

	_, body := browse(t, ts, clone)
	if !body.IsRepo {
		t.Fatalf("browsing %q reports is_repo = false", clone)
	}

	volume := filepath.VolumeName(clone) + string(filepath.Separator)
	_, root := browse(t, ts, volume)
	if root.Parent != "" {
		t.Fatalf("volume root %q reports parent %q, want empty", volume, root.Parent)
	}
}

func TestBrowseRejectsUnusablePaths(t *testing.T) {
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
		{"relative", filepath.Join("Projects", "acme")},
		{"nonexistent", filepath.Join(home, "nope")},
		{"a file", file},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := browse(t, ts, tc.path)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
		})
	}
}

func TestBrowseRejectsNonGET(t *testing.T) {
	ts := browseServer(t, t.TempDir())

	res := postJSON(t, ts.URL+APIPrefix+"/fs/browse", map[string]string{})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.StatusCode)
	}
}

// The browse endpoint hands out the host's directory tree, so it refuses on an
// exposed bind exactly as registration does — a valid bearer token is not enough.
func TestBrowseRefusedOnExposedBind(t *testing.T) {
	ts := exposedServer(t, "0.0.0.0", "secret")

	if got := statusWithToken(t, ts, APIPrefix+"/fs/browse", "secret"); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+APIPrefix+"/fs/browse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET browse: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if !strings.Contains(body["error"], "SERVE_ALLOW_REGISTER") {
		t.Fatalf("refusal = %q, want it to name SERVE_ALLOW_REGISTER", body["error"])
	}
}
