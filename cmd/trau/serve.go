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
	"time"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubdb"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// serveShutdownTimeout bounds the graceful drain once a signal arrives.
const serveShutdownTimeout = 5 * time.Second

// runServe starts the local HTTP hub: the versioned JSON API and the embedded
// web UI. Bind address and port come from the layered config (SERVE_BIND /
// SERVE_PORT), overridable with --bind / --port. It blocks until the context is
// cancelled (Ctrl-C / SIGTERM), then drains connections gracefully.
func runServe(ctx context.Context, args []string, stderr io.Writer) error {
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

	home := registry.Home()
	db, err := hubdb.Open(home)
	if err != nil {
		return console.Actionable(err, "open hub database",
			fmt.Sprintf("move %s aside (mv %s %s.bak) and restart to recreate it", hubdb.Path(home), hubdb.Path(home), hubdb.Path(home)))
	}
	defer db.Close()
	logger.Verbosef("hub database ready at %s (schema v%d)", db.Path(), db.Version())

	addr := net.JoinHostPort(cfg.ServeBind, strconv.Itoa(cfg.ServePort))
	hub := webserver.New(version, cfg.ServeBind, cfg.ServeToken, cfg.ServeWorkspace, cfg.ServeAllowRegister)
	hub.Start(ctx)
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serveShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
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
