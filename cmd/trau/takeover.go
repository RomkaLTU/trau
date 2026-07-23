package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/hubpresence"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/pipeline"
	"github.com/RomkaLTU/trau/internal/registry"
	"github.com/RomkaLTU/trau/internal/state"
	"github.com/RomkaLTU/trau/internal/webserver"
)

// takeoverListTimeout bounds one instance-list read so an unresponsive hub never
// wedges a start path.
const takeoverListTimeout = 10 * time.Second

// runTakeover resumes a parked ticket's recorded claude session in this
// terminal, holding the repo through a takeover presence instance while claude
// runs (ADR 0018). The hub is required: the lock is a presence entry, so no hub
// means no lock and the command refuses rather than running unlocked.
func runTakeover(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var ticket, repo string
	var verbose, debug bool
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
		switch {
		case a == "--repo":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			repo = v
		case a == "--verbose":
			verbose = true
		case a == "--debug":
			debug = true
		case strings.HasPrefix(a, "-"):
			return usageError{fmt.Errorf("takeover: unknown flag: %s", a)}
		case ticket == "":
			ticket = a
		default:
			return usageError{fmt.Errorf("takeover: unexpected arg: %s", a)}
		}
	}
	if ticket == "" {
		return usageError{errors.New("takeover: usage: trau takeover <ID> [--repo <path>]")}
	}
	logger.Init(stderr, verbose, debug)

	repoRoot, err := config.ResolveRepoRoot(repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	if err != nil {
		return console.Actionable(err, "resolve target repo", "pass --repo <path>, set TRAU_REPO_ROOT, or run inside a git repository")
	}
	userEnv := ""
	if home, herr := os.UserHomeDir(); herr == nil {
		userEnv = config.ProjectConfigPath(home)
	}
	cfg, err := config.LoadLayered(config.ProjectConfigPath(repoRoot), userEnv, config.LocalConfigPath(), "")
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	cfg.RepoRoot = repoRoot

	base := hubBaseURL(cfg)
	if !probeHub(ctx, base+webserver.APIPrefix+"/health", cfg.ServeToken).isHub {
		return fmt.Errorf("takeover needs the web hub for the repo lock, and %s is not answering — start it with `trau serve`", base)
	}

	hub := hubclient.New(base, cfg.ServeToken)
	sess := &takeoverSession{
		repoRoot: repoRoot,
		ticket:   ticket,
		pid:      os.Getpid(),
		presence: hubpresence.Register(hub, repoRoot, cfg.RunsDir),
		instances: func(ctx context.Context) ([]hubclient.Instance, error) {
			ictx, cancel := context.WithTimeout(ctx, takeoverListTimeout)
			defer cancel()
			return hub.Instances(ictx)
		},
		cps:           newCheckpointStore(cfg, repoRoot),
		git:           pipeline.ExecGit{Repo: repoRoot},
		sessionExists: agent.SessionExists,
		runClaude:     claudeResumeRunner(cfg.ClaudeBin, repoRoot),
		now:           time.Now,
		out:           stdout,
	}
	return sess.run(ctx)
}

// takeoverPresence is the slice of hubpresence.Handle the takeover lock needs,
// narrowed so a test can substitute a double.
type takeoverPresence interface {
	SetState(state, ticket, phase string)
	Deregister()
}

// takeoverGit is the slice of git a takeover drives: put the repo on the
// session's recorded branch, or read the head it will run on instead.
type takeoverGit interface {
	CurrentBranch(ctx context.Context) (string, error)
	Checkout(ctx context.Context, ref string, force bool) error
}

// takeoverSession is one wired `trau takeover` run: the resolved repo and
// ticket plus every seam the wrapper drives — the presence lock, the hub's
// instance list, the checkpoint store, git, the transcript-existence probe, and
// the interactive claude child.
type takeoverSession struct {
	repoRoot      string
	ticket        string
	pid           int
	presence      takeoverPresence
	instances     func(context.Context) ([]hubclient.Instance, error)
	cps           state.Checkpoints
	git           takeoverGit
	sessionExists func(sessionID string) bool
	runClaude     func(sessionID string) error
	now           func() time.Time
	out           io.Writer
}

// run drives the takeover in order: hold the lock, refuse while a run is still
// active in the repo, resolve the recorded claude session, put the repo on the
// ticket's branch, stamp the checkpoint so the recap shows a human drove this
// run, then hand the terminal to claude and release the lock on exit. Every
// path deregisters, so a refused or failed takeover leaves no lock behind.
func (t *takeoverSession) run(ctx context.Context) error {
	t.presence.SetState(registry.StateTakeover, t.ticket, "")
	defer t.presence.Deregister()

	list, err := t.instances(ctx)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	if in, ok := activeRunFor(list, t.repoRoot, t.pid); ok {
		if in.SessionState == registry.StateTakeover {
			return fmt.Errorf("this repo is already taken over — PID %d holds %s in a terminal session; close it before starting another", in.PID, in.Ticket)
		}
		desc := in.SessionState
		if in.Ticket != "" {
			desc += " " + in.Ticket
		}
		return fmt.Errorf("a run is still active in this repo (PID %d, %s) — stop it before taking over", in.PID, desc)
	}

	sid := t.cps.Get(t.ticket, "SESSION")
	if sid == "" || !t.sessionExists(sid) {
		return fmt.Errorf("no resumable claude session for %s", t.ticket)
	}
	phase := t.cps.Get(t.ticket, "SESSION_PHASE")

	branch, err := t.checkoutRecordedBranch(ctx)
	if err != nil {
		return err
	}

	if err := t.cps.Set(t.ticket, "TAKEOVER", t.now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("stamp takeover on %s: %w", t.ticket, err)
	}
	if err := t.cps.Set(t.ticket, "ANOMALIES", appendAnomaly(t.cps.Get(t.ticket, "ANOMALIES"), "takeover")); err != nil {
		return fmt.Errorf("stamp takeover on %s: %w", t.ticket, err)
	}

	_, _ = fmt.Fprintf(t.out, "Taking over %s on %s — resuming its %s session.\n", t.ticket, branch, phase)
	_, _ = fmt.Fprintln(t.out, "The conversation reopens with its full context and then waits: nothing runs until you type an instruction.")
	_, _ = fmt.Fprintln(t.out, "Closing claude releases the repo.")
	runErr := t.runClaude(sid)
	if runErr != nil {
		_, _ = fmt.Fprintf(t.out, "claude exited with an error: %v\n", runErr)
	}
	_, _ = fmt.Fprintf(t.out, "Released the repo — %s is left checked out and %s stays parked at its %s checkpoint.\n", branch, t.ticket, phase)
	_, _ = fmt.Fprintln(t.out, "Hand the ticket back with Run next in the trau web UI.")
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return silentExit{ee.ExitCode()}
		}
		return fmt.Errorf("run claude: %w", runErr)
	}
	return nil
}

// checkoutRecordedBranch puts the repo on the branch the run committed its WIP
// to and returns it, so whatever the steered agent writes lands where the
// ticket's work lives rather than on the clean base the stop path left behind.
// With no branch recorded the head stays put and is reported instead.
func (t *takeoverSession) checkoutRecordedBranch(ctx context.Context) (string, error) {
	branch := t.cps.Get(t.ticket, "BRANCH")
	if branch == "" {
		head, err := t.git.CurrentBranch(ctx)
		if err != nil {
			return "", console.Actionable(err, "read the current branch of "+t.repoRoot, "check that the repo is a healthy git checkout")
		}
		_, _ = fmt.Fprintf(t.out, "%s has no recorded branch — resuming on the current head, %s.\n", t.ticket, head)
		return head, nil
	}
	if err := t.git.Checkout(ctx, branch, false); err != nil {
		return "", console.Actionable(err,
			fmt.Sprintf("check out %s for %s", branch, t.ticket),
			fmt.Sprintf("commit or stash your changes in %s and retry — the takeover resumes on the ticket's branch, never on the base", t.repoRoot))
	}
	return branch, nil
}

// claudeResumeRunner returns the interactive claude child a takeover hands the
// terminal to: `claude --resume <sid>` in repoRoot on this process's stdio, with
// the user's normal interactive permissions — none of the loop's flags or env.
func claudeResumeRunner(bin, repoRoot string) func(sessionID string) error {
	return func(sessionID string) error {
		cmd := exec.Command(bin, "--resume", sessionID)
		cmd.Dir = repoRoot
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}

// appendAnomaly adds marker to a checkpoint's comma-separated ANOMALIES value,
// leaving an already-recorded marker in place so repeat takeovers stamp once.
func appendAnomaly(current, marker string) string {
	if current == "" {
		return marker
	}
	for _, v := range strings.Split(current, ",") {
		if v == marker {
			return current
		}
	}
	return current + "," + marker
}

// activeRunFor returns the live instance holding repoRoot — a working, grazing,
// or stopping run, or another terminal's takeover — that must end before this
// takeover may hold the repo. selfPID skips the entry this process registered
// for itself. Idle dashboards and parked loops do not block; the web flow stops
// the loop before opening the terminal, so this is a belt-and-braces recheck.
func activeRunFor(instances []hubclient.Instance, repoRoot string, selfPID int) (hubclient.Instance, bool) {
	for _, in := range instances {
		if in.RepoRoot != repoRoot || in.PID == selfPID {
			continue
		}
		switch in.SessionState {
		case registry.StateWorking, registry.StateGrazing, registry.StateStopping, registry.StateTakeover:
			return in, true
		}
	}
	return hubclient.Instance{}, false
}

// takeoverFor returns the live takeover instance holding repoRoot, if any.
func takeoverFor(instances []hubclient.Instance, repoRoot string) (hubclient.Instance, bool) {
	for _, in := range instances {
		if in.RepoRoot == repoRoot && in.SessionState == registry.StateTakeover {
			return in, true
		}
	}
	return hubclient.Instance{}, false
}

// takenOverRefusal is the loop-start guard: the error to refuse with when a
// live takeover terminal holds repoRoot, nil when none does. Best-effort — with
// no reachable hub there is no lock to honor, so a listing failure never blocks
// a start.
func takenOverRefusal(ctx context.Context, hub *hubclient.Client, repoRoot string) error {
	ictx, cancel := context.WithTimeout(ctx, takeoverListTimeout)
	defer cancel()
	list, err := hub.Instances(ictx)
	if err != nil {
		return nil
	}
	in, ok := takeoverFor(list, repoRoot)
	if !ok {
		return nil
	}
	return fmt.Errorf("refusing to start: this repo is taken over — PID %d holds %s in a terminal session; close it to hand the repo back", in.PID, in.Ticket)
}
