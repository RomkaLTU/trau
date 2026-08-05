package registry

import (
	"path/filepath"
	"testing"
)

func TestCanonicalRootFoldsTheSpellingsOfOneDirectory(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join("repos", "acme")
	cases := map[string]string{
		root:                               root,
		root + sep:                         root,
		"repos" + sep + "." + sep + "acme": root,
		"repos" + sep + "web" + sep + ".." + sep + "acme": root,
		"": "",
	}
	for spelling, want := range cases {
		if got := CanonicalRoot(spelling); got != want {
			t.Errorf("CanonicalRoot(%q) = %q, want %q", spelling, got, want)
		}
	}
}
