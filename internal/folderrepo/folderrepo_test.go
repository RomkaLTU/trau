package folderrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChildrenFindsTheGitRepositoriesInsideAFolder(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"api-companies", "api-apigateway"} {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	children, truncated, err := Children(root)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false for a folder well under the cap")
	}
	want := []Child{
		{Name: "api-apigateway", Path: filepath.Join(root, "api-apigateway")},
		{Name: "api-companies", Path: filepath.Join(root, "api-companies")},
	}
	if len(children) != len(want) {
		t.Fatalf("Children = %v, want %v", children, want)
	}
	for i, c := range children {
		if c != want[i] {
			t.Errorf("Children[%d] = %v, want %v", i, c, want[i])
		}
	}
	if !Is(root) {
		t.Error("Is(root) = false, want true for a folder holding git repositories")
	}
	if Is(filepath.Join(root, "api-companies")) {
		t.Error("Is(child) = true, want false for a git repository")
	}
}
