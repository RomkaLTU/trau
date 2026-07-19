package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/hubstore"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/transcriptdb"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// serveShutdownTimeout bounds the graceful drain once a signal arrives.
const serveShutdownTimeout = 5 * time.Second

// runServe starts the local HTTP hub: the versioned JSON API and the embedded
// web UI. Bind address and port come from the layered config (SERVE_BIND /
// SERVE_PORT), overridable with --bind / --port. It blocks until the context is
// cancelled (Ctrl-C / SIGTERM), then drains connections gracefully.
func runServe(ctx context.Context, args []string, stderr io.Writer) (err error) {
	var (
		bind, repo     string
		port           = -1
		verbose, debug bool
	)
	i := 0
	next := func(flag string) (string, error) {
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", flag)
		}
		i++
		return args[i], nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--bind":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			bind = v
		case "--port":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return usageError{fmt.Errorf("serve: invalid --port %q: %w", v, err)}
			}
			port = n
		case "--repo":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			repo = v
		case "--verbose":
			verbose = true
		case "--debug":
			debug = true
		default:
			return usageError{fmt.Errorf("serve: unknown arg: %s", a)}
		}
	}
	logger.Init(stderr, verbose, debug)

	cfg, err := loadServeConfig(repo)
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	if bind != "" {
		cfg.ServeBind = bind
	}
	if port >= 0 {
		cfg.ServePort = port
	}

	if err := webserver.CheckExposure(cfg.ServeBind, cfg.ServeToken); err != nil {
		return console.Actionable(err, "start serve", "set SERVE_TOKEN to a secret, or keep SERVE_BIND on loopback (127.0.0.1)")
	}

	// Registered before the database defers so it runs after them: the successor
	// binds the port and opens the hub databases only once this process has let
	// go of both.
	restarting := false
	defer func() {
		if !restarting {
			return
		}
		if spawnErr := respawnServe(args); spawnErr != nil {
			err = errors.Join(err, console.Actionable(spawnErr, "respawn the hub",
				"install trau so `trau` resolves on PATH, then run `trau serve`"))
		}
	}()

	home := registry.Home()
	db, err := hubdb.Open(home)
	if err != nil {
		return console.Actionable(err, "open hub database",
			fmt.Sprintf("move %s aside (mv %s %s.bak) and restart to recreate it", hubdb.Path(home), hubdb.Path(home), hubdb.Path(home)))
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	logger.Verbosef("hub database ready at %s (schema v%d)", db.Path(), db.Version())

	tdb, err := transcriptdb.Open(home)
	if err != nil {
		return console.Actionable(err, "open transcript database",
			fmt.Sprintf("delete %s and restart to recreate it empty (it holds only transcripts)", transcriptdb.Path(home)))
	}
	defer func() { err = errors.Join(err, tdb.Close()) }()
	logger.Verbosef("transcript database ready at %s (schema v%d)", tdb.Path(), tdb.Version())

	stores := hubstore.NewStores(db.SQL(), tdb.SQL(), hubstore.Retention{
		Transcripts: cfg.TranscriptRetention,
		Events:      cfg.EventRetention,
		TokenCalls:  cfg.TokenRetention,
		Grill:       cfg.GrillRetention,
	})
	if err := stores.Registrations().ImportLegacy(home); err != nil {
		return console.Actionable(err, "import legacy registration state",
			"fix or move the named file aside, then restart trau serve")
	}
	if err := stores.ImportLegacyQueues(); err != nil {
		return console.Actionable(err, "import legacy queue state",
			"fix or move the named queue.json aside, then restart trau serve")
	}

	addr := net.JoinHostPort(cfg.ServeBind, strconv.Itoa(cfg.ServePort))
	hub := webserver.New(version, cfg.ServeBind, cfg.ServeToken, cfg.ServeWorkspace, cfg.ServeAllowRegister, stores)
	grillBase := "http://" + net.JoinHostPort(grillReachableHost(cfg.ServeBind), strconv.Itoa(cfg.ServePort))
	hub.EnableGrilling(ctx, grillBase)
	hub.EnableAtlas(ctx)
	restartCh := make(chan struct{})
	hub.EnableRestart(func() { close(restartCh) })
	hub.Start(ctx, time.Duration(cfg.ServeSyncInterval)*time.Second, time.Duration(cfg.ServeReconcileInterval)*time.Second)
	srv := &http.Server{Addr: addr, Handler: hub.Handler()}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return console.Actionable(err, "bind "+addr, "another process may be using the port; set SERVE_PORT or pass --port")
	}
	_, _ = fmt.Fprintf(stderr, "trau serve listening on http://%s\n", addr)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		return drainServer(srv)
	case <-restartCh:
		restarting = true
		// A restart drops stragglers rather than failing: the listener is closed
		// either way, and open event streams would otherwise hold the drain open
		// for its full timeout. The successor's boot follows this line in hub.log.
		if err := drainServer(srv); err != nil {
			_, _ = fmt.Fprintf(stderr, "trau serve: restart drain cut short after %s (%v)\n", serveShutdownTimeout, err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// drainServer closes the listener and lets in-flight requests finish. It returns
// only once the port is free, which is what makes a successor able to bind it.
func drainServer(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), serveShutdownTimeout)
	defer cancel()
	return srv.Shutdown(ctx)
}

// grillReachableHost is the host a hub-local grilling child dials to reach the
// hub's own API. A wildcard bind (empty, 0.0.0.0, ::) listens on every interface
// but is not itself dialable, so the child uses loopback; any other bind is a
// concrete address it can reach directly.
func grillReachableHost(bind string) string {
	switch strings.TrimSpace(bind) {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	default:
		return bind
	}
}

// loadServeConfig resolves the layered config for the serve command, mirroring
// the loop's repo/user/local precedence but without a provider selector.
func loadServeConfig(repo string) (config.Config, error) {
	repoRoot, _ := config.ResolveRepoRoot(repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	userEnv := ""
	if home, err := os.UserHomeDir(); err == nil {
		userEnv = config.ProjectConfigPath(home)
	}
	return config.LoadLayered(config.ProjectConfigPath(repoRoot), userEnv, config.LocalConfigPath(), "")
}
