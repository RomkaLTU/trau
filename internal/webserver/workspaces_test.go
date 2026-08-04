package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func workspacesServer(t *testing.T, manifests map[string]string) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	root := filepath.Dir(filepath.Dir(seedRepo(t, home, "acme")))
	for rel, content := range manifests {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := New("1.2.3", "127.0.0.1", "", nil, false, testStoresAt(t, home))
	s.home = home
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.URL + APIPrefix + "/repos/acme/workspaces"
}

func getWorkspaces(t *testing.T, url string) WorkspacesResponse {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("get workspaces: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("workspaces status = %d, want 200", res.StatusCode)
	}
	var out WorkspacesResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode workspaces: %v", err)
	}
	return out
}

func TestRepoWorkspaces(t *testing.T) {
	_, url := workspacesServer(t, map[string]string{
		"package.json":          `{"name":"mono","private":true,"workspaces":["apps/*"]}`,
		"apps/web/package.json": `{"name":"@acme/web"}`,
		"apps/api/package.json": `{"private":true}`,
	})

	want := []WorkspaceView{
		{Path: "apps/api", DirName: "api"},
		{Name: "@acme/web", Path: "apps/web", DirName: "web"},
	}
	if got := getWorkspaces(t, url).Workspaces; !reflect.DeepEqual(got, want) {
		t.Errorf("workspaces = %+v, want %+v", got, want)
	}
}

// TestRepoWorkspacesEmpty covers the single-app and non-Node repos: no workspace
// manifests is an empty list, not an error, so the editors just offer nothing.
func TestRepoWorkspacesEmpty(t *testing.T) {
	_, url := workspacesServer(t, map[string]string{"go.mod": "module example.com/solo\n"})

	if got := getWorkspaces(t, url).Workspaces; len(got) != 0 {
		t.Errorf("workspaces = %+v, want empty", got)
	}
}

func TestRepoWorkspacesUnknownRepo(t *testing.T) {
	ts, _ := workspacesServer(t, nil)
	res, err := http.Get(ts.URL + APIPrefix + "/repos/ghost/workspaces")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo status = %d, want 404", res.StatusCode)
	}
}
