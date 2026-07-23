package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/webserver"
)

const steerUsage = `usage: trau steer <ID> <note> [--repo <path>] (pass "-" as the note to read it from stdin)`

// steerArgs is one parsed `trau steer` invocation.
type steerArgs struct {
	ticket  string
	body    string
	repo    string
	verbose bool
	debug   bool
}

// parseSteerArgs reads a steer invocation: the ticket id first, then the note,
// whose trailing words join with spaces or come whole from stdin when the only
// body arg is "-" (a heredoc's multi-line text arrives that way).
func parseSteerArgs(args []string, stdin io.Reader) (steerArgs, error) {
	var (
		a     steerArgs
		words []string
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
		arg := args[i]
		switch {
		case arg == "--repo":
			v, err := next(arg)
			if err != nil {
				return steerArgs{}, usageError{err}
			}
			a.repo = v
		case arg == "--verbose":
			a.verbose = true
		case arg == "--debug":
			a.debug = true
		case arg != "-" && strings.HasPrefix(arg, "-"):
			return steerArgs{}, usageError{fmt.Errorf("steer: unknown flag: %s — %s", arg, steerUsage)}
		case a.ticket == "" && arg != "-":
			a.ticket = arg
		default:
			words = append(words, arg)
		}
	}
	if a.ticket == "" {
		return steerArgs{}, usageError{errors.New("steer: a ticket id is required — " + steerUsage)}
	}

	body := strings.Join(words, " ")
	if len(words) == 1 && words[0] == "-" {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return steerArgs{}, fmt.Errorf("read the steer note from stdin: %w", err)
		}
		body = string(read)
	}
	a.body = strings.TrimSpace(body)
	if a.body == "" {
		return steerArgs{}, usageError{errors.New("steer: a note body is required — " + steerUsage)}
	}
	return a, nil
}

// runSteer queues an operator note against a running ticket — the terminal
// counterpart to the hub's steer box. The hub is deliberately never autostarted:
// nothing serving means no run to steer, so the command refuses instead.
func runSteer(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		_, _ = fmt.Fprintln(stdout, steerUsage)
		return nil
	}
	a, err := parseSteerArgs(args, os.Stdin)
	if err != nil {
		return err
	}
	logger.Init(stderr, a.verbose, a.debug)

	cfg, err := loadServeConfig(a.repo)
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	// cfg.RepoRoot already carries TRAU_REPO_ROOT layered env-over-ini, so it is
	// the middle rung of the ADR 0001 order that --repo has to beat.
	cfg.RepoRoot, _ = config.ResolveRepoRoot(a.repo, cfg.RepoRoot, config.GitToplevel)
	repo := repoName(cfg.RepoRoot)
	if repo == "" {
		return usageError{errors.New("steer: no repo resolved — run inside a git repo or pass --repo <path>")}
	}

	base := hubBaseURL(cfg)
	if !probeHub(ctx, base+webserver.APIPrefix+"/health", cfg.ServeToken).isHub {
		return fmt.Errorf("no hub is answering at %s, so nothing is running to steer — start it with `trau serve`", base)
	}
	return queueSteerNote(ctx, hubclient.New(base, cfg.ServeToken), repo, a.ticket, a.body, stdout)
}

func queueSteerNote(ctx context.Context, hub *hubclient.Client, repo, ticket, body string, stdout io.Writer) error {
	note, err := hub.QueueSteer(ctx, repo, ticket, body)
	if err != nil {
		if errors.Is(err, hubclient.ErrNotFound) {
			return console.Actionable(err, "steer "+ticket,
				"the hub does not know repo "+repo+" — run a loop here first, or pass --repo")
		}
		return fmt.Errorf("queue steer note for %s: %w", ticket, err)
	}
	_, _ = fmt.Fprintf(stdout, "Steer note %d queued for %s — it reaches the agent mid-phase, or at the next spawn.\n", note.ID, note.Ticket)
	return nil
}
