package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveRepoRootFromCwd is the Folder repo resolution rule: a folder root and
// a non-git directory under it both resolve to the folder, and standing inside a
// Child repo resolves to whichever of the child and the folder the hub has
// registered — falling back to the child when it has registered neither or cannot
// be asked at all.
func TestResolveRepoRootFromCwd(t *testing.T) {
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(folder, "api-companies")
	docs := filepath.Join(folder, "docs")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}

	outside := errors.New("not inside a git repository")
	inChild := func() (string, error) { return child, nil }
	notInAnyRepo := func() (string, error) { return "", outside }

	cases := []struct {
		name       string
		cwd        string
		gitTop     func() (string, error)
		registered func() ([]string, error)
		want       string
	}{
		{
			name:   "folder root",
			cwd:    folder,
			gitTop: notInAnyRepo,
			want:   folder,
		},
		{
			name:   "non-git directory under the folder",
			cwd:    docs,
			gitTop: notInAnyRepo,
			want:   folder,
		},
		{
			name:       "registered child wins over its folder",
			cwd:        child,
			gitTop:     inChild,
			registered: func() ([]string, error) { return []string{child, folder}, nil },
			want:       child,
		},
		{
			name:       "unregistered child yields to its registered folder",
			cwd:        child,
			gitTop:     inChild,
			registered: func() ([]string, error) { return []string{folder}, nil },
			want:       folder,
		},
		{
			name:       "no registration store leaves the child",
			cwd:        child,
			gitTop:     inChild,
			registered: func() ([]string, error) { return nil, errors.New("no hub database") },
			want:       child,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			got, err := ResolveRepoRoot("", "", tc.gitTop, tc.registered)
			if err != nil {
				t.Fatalf("ResolveRepoRoot: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveRepoRoot = %q, want %q", got, tc.want)
			}
		})
	}
}
