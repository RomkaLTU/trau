package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layerFixture writes the three file layers into a temp dir and returns their
// paths. An empty body means the layer has no file at all.
func layerFixture(t *testing.T, project, local, user string) LayerPaths {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if body == "" {
			return path
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return LayerPaths{
		Project: write(".trau.ini", project),
		Local:   write("trau.ini", local),
		User:    write("home.trau.ini", user),
	}
}

// unsetEnv neutralizes the ambient environment for keys under test — a loop
// child inherits knobs like CLAUDE_EFFORT, and an env value legitimately makes
// the file layers stop deciding.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		t.Setenv("TRAU_"+k, "")
	}
}

func TestShadowedKeysDetectsEmptyMask(t *testing.T) {
	unsetEnv(t, "CLAUDE_MODEL", "CLAUDE_EFFORT")
	paths := layerFixture(t,
		"",
		"CLAUDE_MODEL=\nCLAUDE_EFFORT=\n",
		"CLAUDE_MODEL=claude-opus-4-8\nCLAUDE_EFFORT=xhigh\n")

	got := ShadowedKeys(paths)
	if len(got) != 2 {
		t.Fatalf("shadowed = %+v, want 2 keys", got)
	}
	if got[0].Key != "CLAUDE_EFFORT" || got[1].Key != "CLAUDE_MODEL" {
		t.Errorf("keys = %q/%q, want them sorted CLAUDE_EFFORT/CLAUDE_MODEL", got[0].Key, got[1].Key)
	}
	m := got[1]
	if m.By != LayerLocal || m.ByPath != paths.Local {
		t.Errorf("shadowing layer = %s (%s), want local (%s)", m.By, m.ByPath, paths.Local)
	}
	if m.Over != LayerUser || m.OverPath != paths.User {
		t.Errorf("shadowed layer = %s (%s), want user (%s)", m.Over, m.OverPath, paths.User)
	}
	if m.Value != "claude-opus-4-8" {
		t.Errorf("value = %q, want the user layer's model", m.Value)
	}
}

// The reported shadowing layer must be the one that actually wins the lookup:
// with two layers blanking the same key, only the highest is worth editing.
func TestShadowedKeysReportsWinningLayer(t *testing.T) {
	unsetEnv(t, "BASE_BRANCH")
	paths := layerFixture(t, "BASE_BRANCH=\n", "BASE_BRANCH=\n", "BASE_BRANCH=develop\n")

	got := ShadowedKeys(paths)
	if len(got) != 1 {
		t.Fatalf("shadowed = %+v, want 1 key", got)
	}
	if got[0].By != LayerProject || got[0].ByPath != paths.Project {
		t.Errorf("shadowing layer = %s (%s), want project (%s)", got[0].By, got[0].ByPath, paths.Project)
	}
}

func TestShadowedKeysRedactsSecrets(t *testing.T) {
	const token = "s3cr3t-token"
	unsetEnv(t, "JIRA_API_TOKEN")
	paths := layerFixture(t, "JIRA_API_TOKEN=\n", "", "JIRA_API_TOKEN="+token+"\n")

	got := ShadowedKeys(paths)
	if len(got) != 1 {
		t.Fatalf("shadowed = %+v, want 1 key", got)
	}
	if got[0].Value != RedactedValue {
		t.Errorf("value = %q, want %q", got[0].Value, RedactedValue)
	}
	if strings.Contains(got[0].Value, token) {
		t.Errorf("shadow report leaked the token: %q", got[0].Value)
	}
}

func TestShadowedKeysQuietCases(t *testing.T) {
	cases := []struct {
		name                 string
		project, local, user string
	}{
		{"non-empty override is ordinary precedence", "BASE_BRANCH=develop\n", "", "BASE_BRANCH=main\n"},
		{"empty in every layer", "BASE_BRANCH=\n", "BASE_BRANCH=\n", "BASE_BRANCH=\n"},
		{"empty with nothing underneath", "BASE_BRANCH=\n", "", ""},
		{"set in one layer only", "", "", "BASE_BRANCH=main\n"},
		{"no config files at all", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnv(t, "BASE_BRANCH")
			if got := ShadowedKeys(layerFixture(t, tc.project, tc.local, tc.user)); len(got) != 0 {
				t.Errorf("shadowed = %+v, want none", got)
			}
		})
	}
}

// An env var outranks every file, so the blank line no longer decides the value
// and there is nothing to warn about.
func TestShadowedKeysSkipsEnvResolvedKeys(t *testing.T) {
	paths := layerFixture(t, "BASE_BRANCH=\nREMOTE=\n", "", "BASE_BRANCH=develop\nREMOTE=upstream\n")

	t.Setenv("BASE_BRANCH", "release")
	t.Setenv("TRAU_REMOTE", "fork")
	if got := ShadowedKeys(paths); len(got) != 0 {
		t.Errorf("shadowed = %+v, want none while env resolves both keys", got)
	}
}

func TestLayerPathsFiles(t *testing.T) {
	paths := layerFixture(t, "", "BASE_BRANCH=main\n", "")
	paths.User = ""

	files := paths.Files()
	if len(files) != 3 {
		t.Fatalf("files = %+v, want 3", files)
	}
	if files[0].Layer != LayerProject || files[1].Layer != LayerLocal || files[2].Layer != LayerUser {
		t.Errorf("layer order = %s/%s/%s, want project/local/user", files[0].Layer, files[1].Layer, files[2].Layer)
	}
	if files[0].Exists {
		t.Errorf("project layer %q reported as existing", files[0].Path)
	}
	if !files[1].Exists {
		t.Errorf("local layer %q should exist", files[1].Path)
	}
	if files[2].Path != "" || files[2].Exists {
		t.Errorf("unresolved user layer = %+v, want empty path", files[2])
	}
}

func TestLayerPathsFilesAbsolute(t *testing.T) {
	files := LayerPaths{Local: LocalConfigName}.Files()
	if !filepath.IsAbs(files[1].Path) {
		t.Errorf("local path = %q, want an absolute path so a stray file is identifiable", files[1].Path)
	}
}
