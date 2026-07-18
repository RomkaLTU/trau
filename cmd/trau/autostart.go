package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
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

	addr := net.JoinHostPort(dialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	healthURL := "http://" + addr + webserver.APIPrefix + "/health"
	webURL := "http://" + addr + "/"

	switch p := probeHub(ctx, healthURL, cfg.ServeToken); {
	case p.isHub:
		if p.version != version {
			_, _ = fmt.Fprintf(stderr, "trau: reusing web UI hub %s (this binary is %s); restart it to update\n", p.version, version)
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
	_, _ = fmt.Fprintf(stderr, "Web UI: %s\n", webURL)

	if !cfg.ServeOpen {
		return
	}
	go func() {
		if waitHubHealthy(ctx, healthURL, cfg.ServeToken) {
			_ = openBrowser(webURL)
		}
	}()
}

// hubBaseURL is the origin of the serve hub the loop reaches over HTTP, derived
// from the configured bind and port with a loopback bind normalized for dialing.
func hubBaseURL(cfg config.Config) string {
	return "http://" + net.JoinHostPort(dialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
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
		_, _ = fmt.Fprintf(stderr, "trau: the web hub did not become ready in time; issue-store operations may fail until it does\n")
	}
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

type hubStatus struct {
	reachable bool
	isHub     bool
	version   string
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
	return hubStatus{reachable: true, isHub: true, version: h.Version}
}

func waitHubHealthy(ctx context.Context, url, token string) bool {
	deadline := time.NewTimer(hubHealthDeadline)
	defer deadline.Stop()
	ticker := time.NewTicker(hubHealthPoll)
	defer ticker.Stop()
	for {
		if probeHub(ctx, url, token).isHub {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

// spawnDetachedServe starts `trau serve` in its own process group so the hub
// outlives the loop that launched it; its net.Listen on the port is the
// singleton lock. nil std streams route the child's output to the null device.
// TRAU_ACTIVE is stripped so the hub — and everything it later spawns — is not
// marked as running inside the loop that autostarted it.
func spawnDetachedServe() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TRAU_ACTIVE=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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

func dialHost(bind string) string {
	switch strings.TrimSpace(bind) {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return bind
}
