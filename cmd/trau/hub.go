package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// hubRestartDeadline bounds the wait for the successor hub to answer health.
// It outlasts a cold boot with database migrations without leaving the user
// staring at a hung command.
const hubRestartDeadline = 15 * time.Second

func runHub(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return usageError{errors.New("hub: missing subcommand (try `trau hub restart`)")}
	}
	switch args[0] {
	case "restart":
		return runHubRestart(ctx, args[1:], stdout)
	default:
		return usageError{fmt.Errorf("hub: unknown subcommand: %s", args[0])}
	}
}

// runHubRestart makes the configured hub current: a running one is asked to
// respawn itself from the on-disk binary, and a missing one is simply started,
// since a fresh hub already runs that binary.
func runHubRestart(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("hub restart: unknown arg: %s", args[0])}
	}
	cfg, err := loadServeConfig("")
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	base := hubBaseURL(cfg)
	healthURL := base + webserver.APIPrefix + "/health"

	before := probeHub(ctx, healthURL, cfg.ServeToken)
	switch {
	case before.isHub:
		if err := postHubRestart(ctx, base, cfg.ServeToken); err != nil {
			return console.Actionable(err, "ask the hub to restart", "see "+hubLogPath())
		}
	case before.reachable || portOccupied(cfg):
		return portBusyError(cfg.ServePort)
	default:
		if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
			return console.Actionable(err, "start the hub", "set SERVE_TOKEN to a secret, or keep SERVE_BIND on loopback (127.0.0.1)")
		}
		if err := spawnDetachedServe(); err != nil {
			return console.Actionable(err, "start the hub", "install trau so `trau` resolves on PATH")
		}
	}

	// The outgoing hub keeps answering health while it drains, so a successor is
	// recognised by its shorter uptime rather than by merely being healthy.
	fresh := func(p hubStatus) bool { return p.uptime < before.uptime }
	if !before.isHub {
		fresh = anyHub
	}
	after, ok := awaitHub(ctx, healthURL, cfg.ServeToken, hubRestartDeadline, fresh)
	if !ok {
		return console.Actionable(fmt.Errorf("the hub did not come back within %s", hubRestartDeadline),
			"restart the hub", "see "+hubLogPath())
	}

	switch {
	case !before.isHub:
		_, _ = fmt.Fprintf(stdout, "hub started (%s)\n", after.version)
	case after.version == before.version:
		_, _ = fmt.Fprintf(stdout, "hub restarted (%s)\n", after.version)
	default:
		_, _ = fmt.Fprintf(stdout, "hub restarted: %s -> %s\n", before.version, after.version)
	}
	return nil
}

// portOccupied reports whether something already holds the hub's port. The
// health probe alone cannot tell a silent listener — one that accepts and then
// never answers — apart from nothing listening at all, and spawning into that
// port only produces a bind failure the caller never sees.
func portOccupied(cfg config.Config) bool {
	addr := net.JoinHostPort(webserver.DialHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	conn, err := net.DialTimeout("tcp", addr, hubProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func postHubRestart(ctx context.Context, base, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+webserver.APIPrefix+"/hub/restart", nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hubHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("hub answered %s", resp.Status)
	}
	return nil
}
