package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
)

// The build channel a hub runs on, derived from where its executable sits: dev
// for a binary inside a registered repo, release for anything a package manager
// or a download put there (ADR 0024).
const (
	channelDev     = "dev"
	channelRelease = "release"
)

// How far a channel switch has got. It never reaches a "done" state: a
// successful switch ends in a restart, and the successor starts idle on dev.
const (
	switchIdle       = "idle"
	switchBuilding   = "building"
	switchRestarting = "restarting"
	switchFailed     = "failed"
)

// channelBuildTimeout bounds the rebuild. A cold `make build` pulls npm
// dependencies and cross-compiles, so it must outlast that without letting a
// wedged build hold the switch open forever.
const channelBuildTimeout = 15 * time.Minute

// channelProbeTimeout bounds `--version` on the freshly built binary.
const channelProbeTimeout = 30 * time.Second

// channelTailLines is how much build output a failure keeps — enough for the
// compiler error that caused it, not the whole log.
const channelTailLines = 20

// ChannelRequest asks the hub to move to a build channel, naming the repo whose
// build it should land on.
type ChannelRequest struct {
	Channel  string `json:"channel"`
	RepoRoot string `json:"repo_root"`
}

// ChannelAck is the answer to an accepted switch: it is under way, not done. The
// rebuild runs on the hub and the outcome arrives on /update's channelSwitch.
type ChannelAck struct {
	Pending  bool   `json:"pending"`
	Channel  string `json:"channel"`
	RepoRoot string `json:"repo_root"`
}

// ChannelSwitch is how a switch is going. Message carries the tail of the build
// output, and only once the switch failed.
type ChannelSwitch struct {
	State    string `json:"state"`
	RepoRoot string `json:"repoRoot"`
	Message  string `json:"message"`
}

// ChannelRepo is one repo the hub could switch onto: a registered repo that has
// opted into being restarted onto its own build.
type ChannelRepo struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

// handleHubChannel rebuilds a registered repo and restarts the hub onto what the
// build produced, which is how a release install moves to the dev channel
// without a terminal. The trust model is the repo's own: it has to be registered
// and to have set HUB_SELF_RELOAD, and on an exposed bind registration has to be
// open too — a switch runs the repo's build command on the host, so it is gated
// exactly as widening the startable set is. The working tree is built as it
// stands: this is the switch a developer asks for, not the post-merge reload.
func (s *Server) handleHubChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.restart == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "this hub cannot restart itself"})
		return
	}
	var req ChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Channel) != channelDev {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `channel must be "dev"`})
		return
	}
	root := strings.TrimSpace(req.RepoRoot)
	if root == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo_root is required"})
		return
	}
	repo, ok := s.findRepo(root)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown repo"})
		return
	}

	projectPath, userPath := s.repoConfigPaths(repo)
	cfg, err := config.LoadLayered(projectPath, userPath, "", "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load repo config: " + err.Error()})
		return
	}
	if !cfg.HubSelfReload {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("self-reload is off for %s; set HUB_SELF_RELOAD=1 in its .trau.ini to allow it", repo.Name),
		})
		return
	}
	if !Loopback(s.bind) && !s.allowRegister {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "switching build channel on an exposed bind requires SERVE_ALLOW_REGISTER=1 in addition to SERVE_TOKEN; set it to open the switch deliberately, or switch from a loopback trau serve on the host",
		})
		return
	}
	if s.supervised {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this hub is supervised by launchd, which restarts it from the binary its plist names; run `trau hub unsupervise` before switching channel",
		})
		return
	}
	if !s.beginChannelSwitch(repo.Root) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a channel switch is already under way"})
		return
	}

	writeJSON(w, http.StatusAccepted, ChannelAck{Pending: true, Channel: channelDev, RepoRoot: repo.Root})
	go s.switchToDev(s.drainCtx, repo, cfg)
}

// switchToDev rebuilds repo and arms a restart onto the binary that build left
// behind. Nothing about the running hub changes until both the build and the
// version probe succeed, so a tree that does not compile leaves it serving.
func (s *Server) switchToDev(ctx context.Context, repo registry.Repo, cfg config.Config) {
	buildCmd := strings.TrimSpace(cfg.HubReloadBuildCmd)
	if buildCmd == "" {
		s.markChannelSwitch(switchFailed, fmt.Sprintf("%s configures no rebuild: set HUB_RELOAD_BUILD_CMD", repo.Name))
		return
	}

	buildCtx, cancel := context.WithTimeout(ctx, channelBuildTimeout)
	out, err := s.runBuild(buildCtx, repo.Root, buildCmd)
	cancel()
	if err != nil {
		logger.Verbosef("channel switch: build %s in %s: %v", buildCmd, repo.Root, err)
		s.markChannelSwitch(switchFailed, fmt.Sprintf("%s failed: %v\n%s", buildCmd, err, tailLines(string(out), channelTailLines)))
		return
	}

	binary, err := s.devBinary(ctx, repo.Root, cfg.HubDevBinary)
	if err != nil {
		s.markChannelSwitch(switchFailed, err.Error())
		return
	}

	s.markChannelSwitch(switchRestarting, "")
	s.triggerRestartTo(binary)
}

// devBinary resolves what the rebuild produced and proves it runs. A binary that
// cannot print its version is not adopted: a restart onto it would take the hub
// down for good, and the release binary the hub is on still works.
func (s *Server) devBinary(ctx context.Context, root, rel string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, channelProbeTimeout)
	defer cancel()

	var lastErr error
	for _, path := range devBinaryPaths(root, rel, s.goos) {
		_, err := s.probeVersion(ctx, path)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("the built binary is unusable: %w", lastErr)
}

// devBinaryPaths is where the build may have left the binary: the configured
// path, and on Windows its .exe twin, since a Makefile that suffixes the
// artifact still reads as bin/trau in config (ADR 0023).
func devBinaryPaths(root, rel, goos string) []string {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if goos != "windows" || strings.EqualFold(filepath.Ext(path), ".exe") {
		return []string{path}
	}
	return []string{path, path + ".exe"}
}

// beginChannelSwitch claims the switch for root, reporting false when one is
// already building or waiting to restart — two rebuilds of the same tree would
// race each other's artifacts.
func (s *Server) beginChannelSwitch(root string) bool {
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if s.channel.State == switchBuilding || s.channel.State == switchRestarting {
		return false
	}
	s.channel = ChannelSwitch{State: switchBuilding, RepoRoot: root}
	return true
}

func (s *Server) markChannelSwitch(state, message string) {
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	s.channel.State, s.channel.Message = state, message
}

// channelSwitch reports the in-flight switch. A hub that has never been asked to
// switch reads idle rather than blank.
func (s *Server) channelSwitch() ChannelSwitch {
	s.channelMu.Lock()
	defer s.channelMu.Unlock()
	if s.channel.State == "" {
		return ChannelSwitch{State: switchIdle}
	}
	return s.channel
}

// hubChannel names the build this hub runs, derived from its executable rather
// than recorded: a binary inside a registered repo is that repo's dev build, and
// the root comes back with it so the UI can name which repo dev means here.
func (s *Server) hubChannel() (channel, root string) {
	exe, err := s.executable()
	if err != nil {
		logger.Verbosef("channel: resolve hub executable: %v", err)
		return channelRelease, ""
	}
	for _, candidate := range s.registeredRoots() {
		if withinRoot(candidate, exe) {
			return channelDev, candidate
		}
	}
	return channelRelease, ""
}

// eligibleChannelRepos lists the repos a switch would be accepted for, so the UI
// offers the action against the same gate the endpoint enforces instead of
// discovering the refusal after the click.
func (s *Server) eligibleChannelRepos() []ChannelRepo {
	repos := []ChannelRepo{}
	for _, root := range s.registeredRoots() {
		cfg, err := repoConfig(root)
		if err != nil || !cfg.HubSelfReload {
			continue
		}
		repos = append(repos, ChannelRepo{Name: filepath.Base(root), Root: root})
	}
	return repos
}

// registeredRoots is every repo root the hub answers for: the repos a loop has
// run in, plus the startable roots the workspace seed and web registrations
// grant. It is the same union findRepo resolves against.
func (s *Server) registeredRoots() []string {
	known := s.knownRepos(s.liveInstances())
	roots := make([]string, 0, len(known))
	for _, repo := range known {
		roots = append(roots, repo.Root)
	}
	return normalizeRoots(append(roots, s.effectiveRoots()...))
}

// runShellCommand hands a repo's own build command to the host shell from its
// root, returning the combined output a failure tail is read from.
func runShellCommand(ctx context.Context, dir, command string) ([]byte, error) {
	cmd := hostShell(ctx, command)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// hostShell hands cmd to the host's shell: sh on unix, and on Windows the
// interpreter COMSPEC names — cmd.exe unless the environment points elsewhere —
// since stock Windows ships no sh.
func hostShell(ctx context.Context, cmd string) *exec.Cmd {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, "sh", "-c", cmd)
	}
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.CommandContext(ctx, shell, "/c", cmd)
}

// tailLines keeps the last n non-empty lines of s, which is what a failed build
// is diagnosed from.
func tailLines(s string, n int) string {
	lines := []string{}
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
