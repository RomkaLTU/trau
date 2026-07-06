package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRegisterRepoPersistsInOrderAndDedupes(t *testing.T) {
	home := t.TempDir()

	if got := RegisteredRepos(home); len(got) != 0 {
		t.Fatalf("fresh home has %d registered, want 0", len(got))
	}

	for _, root := range []string{"/repos/a", "/repos/b", "/repos/a"} {
		if err := RegisterRepo(home, root); err != nil {
			t.Fatalf("RegisterRepo(%q): %v", root, err)
		}
	}

	want := []string{"/repos/a", "/repos/b"}
	if got := RegisteredRepos(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("registered = %v, want %v", got, want)
	}

	if _, err := os.Stat(filepath.Join(home, "workspace.json")); err != nil {
		t.Errorf("workspace.json not written: %v", err)
	}
}

func TestRegisteredReposReadsBackAfterRewrite(t *testing.T) {
	home := t.TempDir()
	if err := RegisterRepo(home, "/repos/a"); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := RegisterRepo(home, "/repos/b"); err != nil {
		t.Fatalf("register b: %v", err)
	}
	want := []string{"/repos/a", "/repos/b"}
	if got := RegisteredRepos(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload = %v, want %v", got, want)
	}
}

func TestRegisterRepoWithoutHomeErrors(t *testing.T) {
	if err := RegisterRepo("", "/repos/a"); err == nil {
		t.Fatal("RegisterRepo with empty home = nil error, want error")
	}
	if got := RegisteredRepos(""); got != nil {
		t.Errorf("RegisteredRepos(\"\") = %v, want nil", got)
	}
}
