package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// releaseExe stands in for a binary no repo owns — a Homebrew install — so a
// hub built on it reads as the release channel.
const releaseExe = "/opt/homebrew/bin/trau"

// channelServer builds a hub allowlisting one repo, running from a release
// binary, with the rebuild and the version probe injected so a switch exercises
// the whole sequence without compiling anything.
func channelServer(t *testing.T, selfReload bool) (*Server, *httptest.Server, string) {
	t.Helper()
	return newChannelServer(t, selfReload, "127.0.0.1", "", false)
}

func newChannelServer(t *testing.T, selfReload bool, bind, token string, allowRegister bool) (*Server, *httptest.Server, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "acme")
	writeReloadConfig(t, root, selfReload)

	s := New("2.1.0", bind, token, []string{root}, allowRegister, testStores(t))
	s.home = t.TempDir()
	s.executable = func() (string, error) { return releaseExe, nil }
	s.runBuild = func(context.Context, string, string) ([]byte, error) { return nil, nil }
	s.probeVersion = func(context.Context, string) (string, error) { return "2.2.0-dev", nil }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, root
}

func postChannel(t *testing.T, ts *httptest.Server, channel, root string) *http.Response {
	t.Helper()
	body, err := json.Marshal(ChannelRequest{Channel: channel, RepoRoot: root})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	res, err := ts.Client().Post(ts.URL+APIPrefix+"/hub/channel", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST channel: %v", err)
	}
	return res
}

// awaitChannelState waits for the async switch to reach want, which is how a
// test observes an outcome the 202 deliberately does not carry.
func awaitChannelState(t *testing.T, s *Server, want string) ChannelSwitch {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := s.channelSwitch()
		if got.State == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("channel switch state = %q, want %q", got.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func errorOf(t *testing.T, res *http.Response) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return payload.Error
}

// TestChannelSwitchRebuildsAndRestartsOntoTheBuild checks the whole sequence: the
// repo's own build command runs in its root, and the restart the hub arms names
// the binary that build produced rather than the release one it is running.
func TestChannelSwitchRebuildsAndRestartsOntoTheBuild(t *testing.T) {
	s, ts, root := channelServer(t, true)
	type build struct{ dir, command string }
	builds := make(chan build, 1)
	s.runBuild = func(_ context.Context, dir, command string) ([]byte, error) {
		builds <- build{dir: dir, command: command}
		return []byte("ok"), nil
	}
	successors := make(chan string, 1)
	s.EnableRestart(func(binary string) { successors <- binary })

	res := postChannel(t, ts, channelDev, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}
	var ack ChannelAck
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !ack.Pending || ack.Channel != channelDev || ack.RepoRoot != root {
		t.Fatalf("ack = %+v, want a pending dev switch for %s", ack, root)
	}

	if got := <-builds; got.dir != root || got.command != "make build" {
		t.Fatalf("build = %+v, want the repo's own command in its root", got)
	}
	want := filepath.Join(root, "bin", "trau")
	if got := <-successors; got != want {
		t.Fatalf("successor = %q, want %q", got, want)
	}
	if got := s.channelSwitch(); got.State != switchRestarting || got.RepoRoot != root {
		t.Fatalf("switch = %+v, want %s for %s", got, switchRestarting, root)
	}
}

// TestChannelSwitchFailedBuildKeepsHubServing checks a tree that does not compile
// leaves the running hub alone and reports the tail of what the build said.
func TestChannelSwitchFailedBuildKeepsHubServing(t *testing.T) {
	s, ts, root := channelServer(t, true)
	var out strings.Builder
	for i := range 40 {
		fmt.Fprintf(&out, "step %d\n", i)
	}
	out.WriteString("main.go:3:2: undefined: nope\n")
	s.runBuild = func(context.Context, string, string) ([]byte, error) {
		return []byte(out.String()), errors.New("exit status 2")
	}
	s.EnableRestart(func(string) { t.Error("a failed build restarted the hub") })

	res := postChannel(t, ts, channelDev, root)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}

	got := awaitChannelState(t, s, switchFailed)
	if !strings.Contains(got.Message, "undefined: nope") {
		t.Errorf("message = %q, want the compiler error", got.Message)
	}
	if strings.Contains(got.Message, "step 0\n") {
		t.Errorf("message = %q, want only the tail of the build output", got.Message)
	}
	if lines := strings.Count(got.Message, "\n") + 1; lines > channelTailLines+1 {
		t.Errorf("message spans %d lines, want the failure line plus at most %d", lines, channelTailLines)
	}
}

// TestChannelSwitchUnusableBinaryIsNotAdopted checks a build that leaves nothing
// runnable behind never becomes the successor — restarting onto it would end the
// hub for good.
func TestChannelSwitchUnusableBinaryIsNotAdopted(t *testing.T) {
	s, ts, root := channelServer(t, true)
	s.probeVersion = func(context.Context, string) (string, error) {
		return "", errors.New("fork/exec: no such file or directory")
	}
	s.EnableRestart(func(string) { t.Error("an unusable binary restarted the hub") })

	res := postChannel(t, ts, channelDev, root)
	_ = res.Body.Close()

	got := awaitChannelState(t, s, switchFailed)
	if !strings.Contains(got.Message, "unusable") {
		t.Errorf("message = %q, want it to name the unusable binary", got.Message)
	}
}

// TestChannelSwitchGates checks each refusal answers with the status and the key
// a caller can act on, and that none of them restarts the hub.
func TestChannelSwitchGates(t *testing.T) {
	const ownRepo = ""
	tests := []struct {
		name       string
		selfReload bool
		channel    string
		repoRoot   string
		want       int
		wantIn     string
	}{
		{name: "unsupported channel", selfReload: true, channel: channelRelease, repoRoot: ownRepo, want: http.StatusBadRequest, wantIn: "dev"},
		{name: "no repo named", selfReload: true, channel: channelDev, repoRoot: "  ", want: http.StatusBadRequest, wantIn: "repo_root"},
		{name: "unregistered repo", selfReload: true, channel: channelDev, repoRoot: "/nowhere/beta", want: http.StatusNotFound, wantIn: "unknown repo"},
		{name: "self-reload off", selfReload: false, channel: channelDev, repoRoot: ownRepo, want: http.StatusForbidden, wantIn: "HUB_SELF_RELOAD"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ts, root := channelServer(t, tt.selfReload)
			s.EnableRestart(func(string) { t.Error("a refused switch restarted the hub") })
			target := tt.repoRoot
			if target == ownRepo {
				target = root
			}

			res := postChannel(t, ts, tt.channel, target)
			defer func() { _ = res.Body.Close() }()

			if res.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.want)
			}
			if msg := errorOf(t, res); !strings.Contains(msg, tt.wantIn) {
				t.Errorf("error = %q, want it to name %q", msg, tt.wantIn)
			}
			if got := s.channelSwitch(); got.State != switchIdle {
				t.Errorf("switch state = %q, want %q after a refusal", got.State, switchIdle)
			}
		})
	}
}

// TestChannelSwitchWithoutSpawnerIsUnavailable checks a hub with no successor to
// spawn says so rather than rebuilding for a restart that will never happen.
func TestChannelSwitchWithoutSpawnerIsUnavailable(t *testing.T) {
	s, ts, root := channelServer(t, true)
	s.runBuild = func(context.Context, string, string) ([]byte, error) {
		t.Error("a hub that cannot restart still ran the build")
		return nil, nil
	}

	res := postChannel(t, ts, channelDev, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestChannelSwitchRejectsGET keeps the endpoint out of reach of a link or a
// prefetch.
func TestChannelSwitchRejectsGET(t *testing.T) {
	s, ts, _ := channelServer(t, true)
	s.EnableRestart(func(string) { t.Error("GET triggered a switch") })

	res, err := http.Get(ts.URL + APIPrefix + "/hub/channel")
	if err != nil {
		t.Fatalf("GET channel: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}
}

// exposedChannelServer builds a token-gated hub on a non-loopback bind, where a
// switch needs registration to be open on top of the token.
func exposedChannelServer(t *testing.T, allowRegister bool) (*Server, *httptest.Server, string) {
	t.Helper()
	return newChannelServer(t, true, "0.0.0.0", "s3cret", allowRegister)
}

func postChannelAuthed(t *testing.T, ts *httptest.Server, root string) *http.Response {
	t.Helper()
	body, err := json.Marshal(ChannelRequest{Channel: channelDev, RepoRoot: root})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+APIPrefix+"/hub/channel", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST channel: %v", err)
	}
	return res
}

// TestChannelSwitchOnExposedBindNeedsAllowRegister checks the switch is gated
// like registration is: a leaked bearer token alone must not be able to rebuild
// and re-exec the hub from a working tree.
func TestChannelSwitchOnExposedBindNeedsAllowRegister(t *testing.T) {
	s, ts, root := exposedChannelServer(t, false)
	s.EnableRestart(func(string) { t.Error("a refused switch restarted the hub") })

	res := postChannelAuthed(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusForbidden)
	}
	if msg := errorOf(t, res); !strings.Contains(msg, "SERVE_ALLOW_REGISTER") {
		t.Errorf("error = %q, want it to name SERVE_ALLOW_REGISTER", msg)
	}
}

// TestChannelSwitchOnExposedBindWithAllowRegister checks the same hub accepts
// once the key deliberately opens it.
func TestChannelSwitchOnExposedBindWithAllowRegister(t *testing.T) {
	s, ts, root := exposedChannelServer(t, true)
	successors := make(chan string, 1)
	s.EnableRestart(func(binary string) { successors <- binary })

	res := postChannelAuthed(t, ts, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}
	if got, want := <-successors, filepath.Join(root, "bin", "trau"); got != want {
		t.Fatalf("successor = %q, want %q", got, want)
	}
}

// TestChannelSwitchRefusesOnSupervisedHub checks a launchd-owned hub says why it
// cannot switch instead of restarting straight back onto the release binary its
// plist names.
func TestChannelSwitchRefusesOnSupervisedHub(t *testing.T) {
	s, ts, root := channelServer(t, true)
	s.EnableRestart(func(string) { t.Error("a supervised hub restarted for a switch") })
	s.SetSupervised(true)

	res := postChannel(t, ts, channelDev, root)
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
	if msg := errorOf(t, res); !strings.Contains(msg, "launchd") {
		t.Errorf("error = %q, want it to name launchd", msg)
	}
}

// TestChannelSwitchRefusesASecondSwitch checks a rebuild already under way is not
// joined by a second one racing it for the same artifacts.
func TestChannelSwitchRefusesASecondSwitch(t *testing.T) {
	s, ts, root := channelServer(t, true)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	s.runBuild = func(context.Context, string, string) ([]byte, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	}
	s.EnableRestart(func(string) {})
	t.Cleanup(func() { close(release) })

	first := postChannel(t, ts, channelDev, root)
	_ = first.Body.Close()
	<-started

	second := postChannel(t, ts, channelDev, root)
	defer func() { _ = second.Body.Close() }()

	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", second.StatusCode, http.StatusConflict)
	}
}

// TestUpdateStatusReportsChannel checks the channel is derived from where the
// hub's executable sits rather than recorded anywhere, and that the owning repo
// comes back with it.
func TestUpdateStatusReportsChannel(t *testing.T) {
	tests := []struct {
		name        string
		insideRepo  bool
		wantChannel string
	}{
		{name: "release install", insideRepo: false, wantChannel: channelRelease},
		{name: "repo build", insideRepo: true, wantChannel: channelDev},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ts, root := channelServer(t, true)
			if tt.insideRepo {
				s.executable = func() (string, error) { return filepath.Join(root, "bin", "trau"), nil }
			}

			res, body := get(t, ts, APIPrefix+"/update")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /update = %d, want 200 (%s)", res.StatusCode, body)
			}
			var got UpdateStatus
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}

			if got.Channel != tt.wantChannel {
				t.Errorf("channel = %q, want %q", got.Channel, tt.wantChannel)
			}
			wantRepo := ""
			if tt.insideRepo {
				wantRepo = root
			}
			if got.ChannelRepo != wantRepo {
				t.Errorf("channelRepo = %q, want %q", got.ChannelRepo, wantRepo)
			}
			if got.ChannelSwitch.State != switchIdle {
				t.Errorf("channelSwitch.state = %q, want %q", got.ChannelSwitch.State, switchIdle)
			}
		})
	}
}

// TestUpdateStatusListsEligibleRepos checks the UI is offered exactly the repos
// the endpoint would accept, so an ineligible repo never reaches a click.
func TestUpdateStatusListsEligibleRepos(t *testing.T) {
	tests := []struct {
		name       string
		selfReload bool
		want       int
	}{
		{name: "opted in", selfReload: true, want: 1},
		{name: "not opted in", selfReload: false, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ts, root := channelServer(t, tt.selfReload)

			_, body := get(t, ts, APIPrefix+"/update")
			var got UpdateStatus
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}

			if len(got.ChannelRepos) != tt.want {
				t.Fatalf("channelRepos = %+v, want %d entry/entries", got.ChannelRepos, tt.want)
			}
			if tt.want > 0 && got.ChannelRepos[0].Root != root {
				t.Errorf("channelRepos[0].root = %q, want %q", got.ChannelRepos[0].Root, root)
			}
		})
	}
}

// TestDevBinaryPaths checks Windows also gets the .exe twin of the configured
// path, since a Makefile that suffixes the artifact still reads as bin/trau in
// config (ADR 0023).
func TestDevBinaryPaths(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		goos string
		want []string
	}{
		{name: "unix", rel: "bin/trau", goos: "darwin", want: []string{filepath.Join("root", "bin", "trau")}},
		{
			name: "windows",
			rel:  "bin/trau",
			goos: "windows",
			want: []string{filepath.Join("root", "bin", "trau"), filepath.Join("root", "bin", "trau") + ".exe"},
		},
		{name: "windows already suffixed", rel: "bin/trau.exe", goos: "windows", want: []string{filepath.Join("root", "bin", "trau.exe")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := devBinaryPaths("root", tt.rel, tt.goos)
			if len(got) != len(tt.want) {
				t.Fatalf("paths = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("paths = %q, want %q", got, tt.want)
				}
			}
		})
	}
}
