package pipeline

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// TestLoadAppURLs pins the hub-wins-wholesale rule: any stored entry replaces
// both configured values at once, and nothing stored — or an unreachable hub —
// leaves APP_URL/APP_URLS exactly as configured.
func TestLoadAppURLs(t *testing.T) {
	cases := []struct {
		name          string
		fetch         func(context.Context) ([]hubclient.AppURL, error)
		wantURL       string
		wantWorkspace map[string]string
	}{
		{
			name:          "no hook keeps the ini values",
			wantURL:       "http://ini:3000",
			wantWorkspace: map[string]string{"web": "http://ini:3001"},
		},
		{
			name:          "no entries keeps the ini values",
			fetch:         func(context.Context) ([]hubclient.AppURL, error) { return nil, nil },
			wantURL:       "http://ini:3000",
			wantWorkspace: map[string]string{"web": "http://ini:3001"},
		},
		{
			name: "unreachable hub keeps the ini values",
			fetch: func(context.Context) ([]hubclient.AppURL, error) {
				return nil, errors.New("hub down")
			},
			wantURL:       "http://ini:3000",
			wantWorkspace: map[string]string{"web": "http://ini:3001"},
		},
		{
			name: "hub entries replace both values",
			fetch: func(context.Context) ([]hubclient.AppURL, error) {
				return []hubclient.AppURL{
					{ID: 1, URL: "http://hub:3000"},
					{ID: 2, URL: "http://hub:3002", Workspace: "api"},
				}, nil
			},
			wantURL:       "http://hub:3000",
			wantWorkspace: map[string]string{"api": "http://hub:3002"},
		},
		{
			name: "workspace-only entries drop the ini default",
			fetch: func(context.Context) ([]hubclient.AppURL, error) {
				return []hubclient.AppURL{{ID: 7, URL: "http://hub:3002", Workspace: "api"}}, nil
			},
			wantWorkspace: map[string]string{"api": "http://hub:3002"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
			p.AppURL = "http://ini:3000"
			p.AppURLs = map[string]string{"web": "http://ini:3001"}
			p.FetchAppURLs = tc.fetch

			p.loadAppURLs(context.Background())

			if p.AppURL != tc.wantURL {
				t.Errorf("AppURL = %q, want %q", p.AppURL, tc.wantURL)
			}
			if !maps.Equal(p.AppURLs, tc.wantWorkspace) {
				t.Errorf("AppURLs = %v, want %v", p.AppURLs, tc.wantWorkspace)
			}
		})
	}
}

// TestLoadAppURLsResolvesFromTheIniValuesEachRun guards the session-long
// pipeline: once a run has installed the hub's entries, the next ticket must
// still see the configured values when the hub has nothing left to serve.
func TestLoadAppURLsResolvesFromTheIniValuesEachRun(t *testing.T) {
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.AppURL = "http://ini:3000"
	entries := []hubclient.AppURL{{ID: 1, URL: "http://hub:3000"}}
	p.FetchAppURLs = func(context.Context) ([]hubclient.AppURL, error) { return entries, nil }

	p.loadAppURLs(context.Background())
	if p.AppURL != "http://hub:3000" {
		t.Fatalf("AppURL = %q, want the hub entry", p.AppURL)
	}

	entries = nil
	p.loadAppURLs(context.Background())

	if p.AppURL != "http://ini:3000" {
		t.Errorf("AppURL = %q, want the configured value back once the hub holds none", p.AppURL)
	}
}

// TestSliceAppURLUsesHubWorkspaceEntries drives the whole resolution the browser
// gate sees: the hub's workspace entry wins for a slice under that workspace, and
// a slice under no configured workspace falls open to the hub's default entry.
func TestSliceAppURLUsesHubWorkspaceEntries(t *testing.T) {
	root := appURLMonorepo(t)
	p := newTestPipeline(t, fakeRunner{}, &fakeTracker{})
	p.RepoRoot = root
	p.AppURL = "http://ini:3000"
	p.FetchAppURLs = func(context.Context) ([]hubclient.AppURL, error) {
		return []hubclient.AppURL{
			{ID: 1, URL: "http://hub:3000"},
			{ID: 2, URL: "http://hub:3001", Workspace: "web"},
		}, nil
	}
	p.loadAppURLs(context.Background())

	p.Git = filesGit{files: []string{"apps/web/src/page.tsx"}}
	if got := p.sliceAppURL(context.Background()); got != "http://hub:3001" {
		t.Errorf("web slice targets %q, want the hub's web workspace entry", got)
	}

	p.Git = filesGit{files: []string{"internal/pipeline/pipeline.go"}}
	if got := p.sliceAppURL(context.Background()); got != "http://hub:3000" {
		t.Errorf("unmatched slice targets %q, want the hub's default entry", got)
	}
}

func appURLMonorepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"package.json":          `{"name":"mono","private":true,"workspaces":["apps/*"]}`,
		"apps/web/package.json": `{"name":"@acme/web"}`,
		"apps/api/package.json": `{"name":"@acme/api"}`,
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
