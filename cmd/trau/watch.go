package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/console"
	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/vterm"
	"github.com/charmbracelet/x/ansi"
)

const (
	// watchPoll is how often the live transcript is re-read and (on a TTY) repainted.
	watchPoll = 200 * time.Millisecond
	// watchSnapshot caps how often a non-TTY watch appends a fresh screen, so a
	// piped/CI log gets a readable heartbeat instead of every spinner frame.
	watchSnapshot = 2 * time.Second
	// watchPollTimeout bounds one hub poll so an unreachable hub never wedges the tick.
	watchPollTimeout = 3 * time.Second
)

// runWatch tails a running loop's live agent transcript and renders it legibly —
// the headless counterpart to the TUI `w` toggle. It is strictly read-only: it
// only polls the hub's transcript chunk store and never touches loop state, so it
// can run in a second terminal alongside a `--no-tui` loop without perturbing it.
//
// With no target it follows the newest active session, re-resolving each tick so it
// tracks phase boundaries. An explicit --id (transcript stem) pins it to one
// session instead.
func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var (
		id, repo       string
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
		case "--id":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			id = v
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
			return usageError{fmt.Errorf("watch: unknown arg: %s", a)}
		}
	}
	logger.Init(stderr, verbose, debug)

	cfg, err := loadServeConfig(repo)
	if err != nil {
		return console.Actionable(err, "load config", "check trau.ini, ~/.trau.ini, and environment variables")
	}
	name := repoName(cfg.RepoRoot)
	if name == "" {
		name = repo
	}

	w := &watcher{
		out:    stdout,
		status: stderr,
		hub:    hubclient.New(hubBaseURL(cfg), cfg.ServeToken),
		repo:   name,
		curID:  id,
		follow: id == "",
		seq:    -1,
		isTTY:  console.IsTerminal(stdout),
	}
	return w.run(ctx)
}

// watcher reconstructs an agent's live screen from the hub's transcript chunks and
// renders it: an in-place repaint on a TTY, throttled appended snapshots when
// piped. It holds a single virtual terminal that is replaced whenever the followed
// session changes (a new phase).
type watcher struct {
	out    io.Writer
	status io.Writer
	hub    *hubclient.Client
	repo   string
	curID  string // followed session; when follow is false, the pinned target
	follow bool
	isTTY  bool

	screen    *vterm.Screen
	cols      int
	rows      int
	seq       int64
	waiting   bool
	lastFrame string
	lastPrint time.Time
}

func (w *watcher) run(ctx context.Context) error {
	if w.isTTY {
		_, _ = io.WriteString(w.out, "\033[?25l") // hide cursor while repainting
		defer func() { _, _ = io.WriteString(w.out, "\033[?25h\r\n") }()
	}
	defer func() {
		if w.screen != nil {
			w.screen.Close()
		}
	}()

	t := time.NewTicker(watchPoll)
	defer t.Stop()
	for {
		w.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (w *watcher) tick(ctx context.Context) {
	pollCtx, cancel := context.WithTimeout(ctx, watchPollTimeout)
	poll, err := w.hub.TranscriptChunks(pollCtx, w.repo, w.curID, w.seq, w.follow, 0)
	cancel()
	if err != nil || poll.ID == "" {
		w.announceWaiting()
		return
	}
	w.waiting = false
	if poll.ID != w.curID || w.screen == nil {
		w.switchTo(poll.ID, poll.Cols, poll.Rows)
	}
	w.seq = poll.Seq
	if len(poll.Data) > 0 {
		w.screen.Write(poll.Data)
		w.render()
	}
}

// announceWaiting prints the "waiting" state once per stall, on stderr so a piped
// stdout stays a clean transcript.
func (w *watcher) announceWaiting() {
	if w.waiting {
		return
	}
	w.waiting = true
	_, _ = fmt.Fprintln(w.status, "⏳ waiting for agent output…")
}

// switchTo points the watcher at a session with a fresh screen, read from the top
// so the current frame reconstructs in full.
func (w *watcher) switchTo(id string, cols, rows int) {
	if w.screen != nil {
		w.screen.Close()
	}
	w.cols, w.rows = cols, rows
	w.screen = vterm.New(w.cols, w.rows)
	w.curID = id
	w.lastFrame = ""
	if !w.isTTY {
		_, _ = fmt.Fprintf(w.out, "── %s ──\n", id)
	}
}

func (w *watcher) render() {
	lines := trimTrailingBlank(w.screen.Lines())
	if w.isTTY {
		w.repaint(lines)
		return
	}
	w.snapshot(lines)
}

// repaint redraws the screen in place: home the cursor, overwrite each line, then
// clear anything left below from a taller previous frame.
func (w *watcher) repaint(lines []string) {
	var b strings.Builder
	b.WriteString("\033[H")
	b.WriteString("\033[K" + "\033[2m▶ watch " + w.curID + " · ctrl-c to stop\033[0m\r\n")
	for _, ln := range lines {
		b.WriteString("\033[K")
		b.WriteString(ln)
		b.WriteString("\r\n")
	}
	b.WriteString("\033[J")
	_, _ = io.WriteString(w.out, b.String())
}

// snapshot appends the current screen to a piped stdout, deduped and throttled so
// the log stays readable instead of capturing every spinner repaint.
func (w *watcher) snapshot(lines []string) {
	frame := strings.Join(lines, "\n")
	if frame == "" || frame == w.lastFrame {
		return
	}
	if !w.lastPrint.IsZero() && time.Since(w.lastPrint) < watchSnapshot {
		return
	}
	w.lastFrame = frame
	w.lastPrint = time.Now()
	_, _ = fmt.Fprintln(w.out, frame)
	_, _ = fmt.Fprintln(w.out)
}

// trimTrailingBlank drops trailing all-blank rows (ANSI aside) so a mostly-empty
// 80x24 screen renders compactly.
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	return lines[:end]
}
