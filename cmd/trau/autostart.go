package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/webserver"
)

const (
	hubProbeTimeout   = 800 * time.Millisecond
	hubHealthDeadline = 4 * time.Second
	hubHealthPoll     = 100 * time.Millisecond
)

var hubHTTP = &http.Client{Timeout: hubProbeTimeout}

// maybeAutostartHub brings the web UI hub up for an interactive TUI session when
// none is already listening. Best-effort: any failure only skips the hub and
// leaves the loop untouched. See ADR 0004.
func maybeAutostartHub(ctx context.Context, cfg config.Config, noServe bool, stderr io.Writer) {
	if noServe || !cfg.ServeAutostart {
		return
	}
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		_, _ = fmt.Fprintf(stderr, "trau: web UI autostart skipped (%v)\n", err)
		return
	}

	addr := net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	healthURL := "http://" + addr + webserver.APIPrefix + "/health"
	webURL := "http://" + addr + "/"

	switch p := probeHub(ctx, healthURL, cfg.ServeToken); {
	case p.isHub:
		if p.version != version {
			_, _ = fmt.Fprintf(stderr, "trau: reusing web UI hub %s (this binary is %s); run 'trau hub restart' to update\n", p.version, version)
		}
		_, _ = fmt.Fprintf(stderr, "Web UI: %s\n", webURL)
		return
	case p.reachable:
		_, _ = fmt.Fprintf(stderr, "trau: web UI autostart skipped (port %d is busy); set SERVE_PORT or SERVE_AUTOSTART=0\n", cfg.ServePort)
		return
	}

	if err := spawnDetachedServe(); err != nil {
		_, _ = fmt.Fprintf(stderr, "trau: web UI autostart failed: %v\n", err)
		return
	}
	if !waitHubHealthy(ctx, healthURL, cfg.ServeToken) {
		reportHubStartFailure(stderr)
		return
	}
	_, _ = fmt.Fprintf(stderr, "Web UI: %s\n", webURL)

	if cfg.ServeOpen {
		_ = openBrowser(webURL)
	}
}

// hubBaseURL is the origin of the serve hub the loop reaches over HTTP, derived
// from the configured bind and port with a loopback bind normalized for dialing.
func hubBaseURL(cfg config.Config) string {
	return "http://" + net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
}

// ensureHubForStore guarantees the serve hub the issue store depends on is
// reachable before the loop reads or writes an issue — the internal provider always,
// and a synced provider whose reads come from the store (ADR 0007). It probes the
// configured hub; if none answers it autostarts one (subject to SERVE_AUTOSTART and
// the exposure policy) and waits for it to become healthy. Best-effort: a hub that
// cannot be brought up leaves the provider to fail its first call with a clear error
// rather than aborting the run here.
func ensureHubForStore(ctx context.Context, cfg config.Config, stderr io.Writer) {
	healthURL := hubBaseURL(cfg) + webserver.APIPrefix + "/health"
	if probeHub(ctx, healthURL, cfg.ServeToken).isHub {
		return
	}
	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		_, _ = fmt.Fprintf(stderr, "trau: the issue store needs the web hub, but it can't autostart (%v)\n", err)
		return
	}
	if !cfg.ServeAutostart {
		_, _ = fmt.Fprintf(stderr, "trau: the issue store needs the web hub — start it with `trau serve` or set SERVE_AUTOSTART=1\n")
		return
	}
	if err := spawnDetachedServe(); err != nil {
		_, _ = fmt.Fprintf(stderr, "trau: could not autostart the web hub for the issue store: %v\n", err)
		return
	}
	if !waitHubHealthy(ctx, healthURL, cfg.ServeToken) {
		reportHubStartFailure(stderr)
	}
}

func reportHubStartFailure(stderr io.Writer) {
	_, _ = fmt.Fprintf(stderr, "trau: web hub failed to start — see %s, or run 'trau serve' to see why\n", hubLogPath())
}

// resolveRepoError wraps a repo-resolution failure for the CLI. When a hub is
// already listening, the suggestion names its URL so the exit doesn't read as
// if the web UI never came up.
func resolveRepoError(ctx context.Context, cfg config.Config, err error) error {
	suggestion := "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository"
	if probeHub(ctx, hubBaseURL(cfg)+webserver.APIPrefix+"/health", cfg.ServeToken).isHub {
		suggestion = "web UI is running at " + hubBaseURL(cfg) + "/ — cd into a repository or pass --repo <path>"
	}
	return console.Actionable(err, "resolve target repo", suggestion)
}

// HubStatus backs the TUI's Web indicator: the configured hub origin plus a
// live health probe.
func (a *appActions) HubStatus(ctx context.Context) (string, bool) {
	base := hubBaseURL(a.cfg)
	return base, probeHub(ctx, base+webserver.APIPrefix+"/health", a.cfg.ServeToken).isHub
}

// OpenWebUI makes the hub reachable for the TUI's Open Web UI action,
// mirroring maybeAutostartHub's policy but returning the reason instead of
// logging it: a healthy hub opens regardless of autostart settings; a down hub
// is autostarted when allowed; otherwise the error says why.
func (a *appActions) OpenWebUI(ctx context.Context) (string, error) {
	base := hubBaseURL(a.cfg)
	healthURL := base + webserver.APIPrefix + "/health"
	webURL := base + "/"
	switch p := probeHub(ctx, healthURL, a.cfg.ServeToken); {
	case p.isHub:
		return webURL, nil
	case p.reachable:
		return "", portBusyError(a.cfg.ServePort)
	}
	if a.opts.NoServe {
		return "", errors.New("hub autostart is off for this session (--no-serve) — run 'trau serve'")
	}
	if !a.cfg.ServeAutostart {
		return "", errors.New("hub autostart is off (SERVE_AUTOSTART=0) — run 'trau serve'")
	}
	if err := webserver.CheckExposure(a.cfg.ServeBind, a.cfg.ServeToken); err != nil {
		return "", fmt.Errorf("hub autostart blocked: %w", err)
	}
	if err := spawnDetachedServe(); err != nil {
		return "", fmt.Errorf("hub spawn failed: %w", err)
	}
	if !waitHubHealthy(ctx, healthURL, a.cfg.ServeToken) {
		return "", fmt.Errorf("web hub failed to start — see %s", hubLogPath())
	}
	return webURL, nil
}

type hubStatus struct {
	reachable bool
	isHub     bool
	version   string
	uptime    float64
}

func probeHub(ctx context.Context, url, token string) hubStatus {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return hubStatus{}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return hubStatus{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return hubStatus{reachable: true}
	}
	var h webserver.Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h); err != nil || h.Status != "ok" {
		return hubStatus{reachable: true}
	}
	return hubStatus{reachable: true, isHub: true, version: h.Version, uptime: h.UptimeSeconds}
}

// awaitHub polls health until a hub answers and fresh accepts it, or deadline
// passes.
func awaitHub(ctx context.Context, url, token string, deadline time.Duration, fresh func(hubStatus) bool) (hubStatus, bool) {
	expiry := time.NewTimer(deadline)
	defer expiry.Stop()
	ticker := time.NewTicker(hubHealthPoll)
	defer ticker.Stop()
	for {
		if p := probeHub(ctx, url, token); p.isHub && fresh(p) {
			return p, true
		}
		select {
		case <-ctx.Done():
			return hubStatus{}, false
		case <-expiry.C:
			return hubStatus{}, false
		case <-ticker.C:
		}
	}
}

func anyHub(hubStatus) bool { return true }

func waitHubHealthy(ctx context.Context, url, token string) bool {
	_, ok := awaitHub(ctx, url, token, hubHealthDeadline, anyHub)
	return ok
}

func portBusyError(port int) error {
	return fmt.Errorf("port %d is busy with something that isn't the hub — set SERVE_PORT", port)
}

// spawnDetachedServe starts a hub from a cold start, truncating hub.log so the
// file only ever holds the latest boot's output. It passes no flags: a cold
// start has none to inherit and the config decides where the hub listens.
func spawnDetachedServe() error { return spawnServe(os.O_TRUNC, nil) }

// respawnServe starts the successor of a hub that is shutting down, replaying
// serveArgs — the outgoing hub's own `serve` flags — so the successor lands on
// the same port, bind and repo instead of falling back to the config defaults.
// It appends to hub.log instead of truncating so the outgoing hub's tail
// survives next to the successor's boot output for diagnosis.
func respawnServe(serveArgs []string) error { return spawnServe(os.O_APPEND, serveArgs) }

// spawnServe starts `trau serve` in its own process group so the hub outlives
// the process that launched it; its net.Listen on the port is the singleton
// lock. TRAU_ACTIVE is stripped so the hub — and everything it later spawns —
// is not marked as running inside the loop that autostarted it.
func spawnServe(logMode int, serveArgs []string) error {
	exe, err := resolveTrauBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, append([]string{"serve"}, serveArgs...)...)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if logFile := openHubLog(logMode); logFile != nil {
		defer func() { _ = logFile.Close() }()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func hubLogPath() string {
	home := registry.Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "hub.log")
}

// openHubLog opens hub.log in logMode (os.O_TRUNC or os.O_APPEND), creating the
// trau home if needed. nil sends the child's output to the null device instead —
// a hub is better spawned unlogged than not spawned.
func openHubLog(logMode int) *os.File {
	path := hubLogPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|logMode, 0o644)
	if err != nil {
		return nil
	}
	return f
}

// resolveTrauBinary picks the binary a spawned hub should run.
func resolveTrauBinary() (string, error) {
	exe, _ := os.Executable()
	return resolveTrauBinaryFrom(exe)
}

// resolveTrauBinaryFrom resolves exe, the running process's own path, to a
// binary that still exists. exe wins when it does — that covers dev builds
// outside PATH, and the stable /opt/homebrew/bin/trau symlink, which after an
// upgrade already points at the new version. `brew upgrade --cask trau` deletes
// the old versioned Caskroom directory, so a process whose path led into it has
// nothing to re-exec and falls back to whatever `trau` resolves to on PATH now.
func resolveTrauBinaryFrom(exe string) (string, error) {
	if exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	path, err := exec.LookPath("trau")
	if err != nil {
		return "", fmt.Errorf("no trau binary to run: %q is gone: %w", exe, err)
	}
	return path, nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
