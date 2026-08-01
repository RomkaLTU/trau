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

// TestCollidesPairsAFolderWithTheRepositoriesInsideIt is the worktree-sharing
// rule both the hub and the CLI refuse runs on: a Folder repo and a repository
// inside it collide in either direction, while two siblings and a root against
// itself do not.
func TestCollidesPairsAFolderWithTheRepositoriesInsideIt(t *testing.T) {
	root := t.TempDir()
	children := []string{"api-companies", "api-billing"}
	for _, name := range children {
		if err := os.MkdirAll(filepath.Join(root, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	companies := filepath.Join(root, "api-companies")
	billing := filepath.Join(root, "api-billing")

	if !Collides(root, companies) {
		t.Error("a folder repo does not collide with a repository inside it")
	}
	if !Collides(companies, root) {
		t.Error("a child repo does not collide with the folder repo holding it")
	}
	if Collides(companies, billing) {
		t.Error("two sibling child repos collide, but they share no working tree")
	}
	if Collides(root, root) {
		t.Error("a root collides with itself, but that is the same repo")
	}
}
