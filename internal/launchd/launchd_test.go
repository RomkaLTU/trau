package launchd

import (
	"slices"
	"strings"
	"testing"
)

func TestPlistCarriesKeepAliveAndEnvironment(t *testing.T) {
	raw := string(plist("/opt/homebrew/bin/trau", []string{"serve"}, map[string]string{
		"TRAU_SUPERVISED": "1",
		"PATH":            "/opt/homebrew/bin:/usr/bin",
	}, "/Users/me/.trau/hub.log"))

	for _, want := range []string{
		"<key>Label</key>\n\t<string>" + Label + "</string>",
		"<string>/opt/homebrew/bin/trau</string>",
		"<string>serve</string>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>TRAU_SUPERVISED</key>\n\t\t<string>1</string>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<key>StandardErrorPath</key>\n\t<string>/Users/me/.trau/hub.log</string>",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("plist is missing %q:\n%s", want, raw)
		}
	}
}

// TestPlistEscapesTheBinaryPath keeps a path with XML metacharacters from
// producing a plist launchd refuses to parse.
func TestPlistEscapesTheBinaryPath(t *testing.T) {
	raw := string(plist("/Users/a&b/trau", nil, nil, ""))
	if !strings.Contains(raw, "<string>/Users/a&amp;b/trau</string>") {
		t.Errorf("plist did not escape the path:\n%s", raw)
	}
	if strings.Contains(raw, "EnvironmentVariables") || strings.Contains(raw, "StandardOutPath") {
		t.Errorf("plist should omit empty sections:\n%s", raw)
	}
}

// TestProgramArgumentsRoundTrip proves the reader doctor reports from and the
// writer Install uses agree, so an installed agent can always be described.
func TestProgramArgumentsRoundTrip(t *testing.T) {
	raw := plist("/usr/local/bin/trau", []string{"serve"}, map[string]string{"PATH": "/usr/bin"}, "/tmp/hub.log")
	got := programArguments(raw)
	if len(got) != 2 || got[0] != "/usr/local/bin/trau" || got[1] != "serve" {
		t.Fatalf("programArguments = %v, want the binary and serve", got)
	}
	if programArguments([]byte("not a plist")) != nil {
		t.Error("unparseable input should read as no program, not a panic")
	}
}

// TestWriteRetargetsTheInstalledProgram covers the rewrite a channel switch
// performs while the hub it belongs to is still running: the plist doctor reads
// names the chosen build, and replacing the job launchd holds is left to Reload.
func TestWriteRetargetsTheInstalledProgram(t *testing.T) {
	if !Supported() {
		t.Skip("launchd is macOS only")
	}
	t.Setenv("HOME", t.TempDir())
	env := map[string]string{"TRAU_SUPERVISED": "1"}
	if err := Write("/opt/homebrew/bin/trau", []string{"serve"}, env, "/tmp/hub.log"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Write("/src/acme/bin/trau", []string{"serve"}, env, "/tmp/hub.log"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	st, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !st.Installed || !slices.Equal(st.Program, []string{"/src/acme/bin/trau", "serve"}) {
		t.Fatalf("Read = %+v, want the agent pointed at the rewritten build", st)
	}
}

func TestReadWithoutAPlistIsTheDefaultState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.Installed || st.Loaded {
		t.Fatalf("Read = %+v, want an uninstalled agent", st)
	}
}
