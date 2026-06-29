package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/agent"
	"github.com/RomkaLTU/trau/internal/config"
	"github.com/RomkaLTU/trau/internal/console"
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
)

// runWatch tails a running loop's live agent transcript and renders it legibly —
// the headless counterpart to the TUI `w` toggle. It is strictly read-only: it
// only opens transcripts for reading and never touches loop state, so it can run
// in a second terminal alongside a `--no-tui` loop without perturbing it.
//
// With no target it follows the newest active transcript under
// <RunsDir>/_agent-results, re-resolving each tick so it tracks phase boundaries
// (each phase writes a new file). An explicit --id (transcript stem) or a path
// pins it to one transcript instead.
func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	var (
		id, path, repo string
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
		switch {
		case a == "--id":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			id = v
		case a == "--path":
			v, err := next(a)
			if err != nil {
				return usageError{err}
			}
			path = v
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
		case !strings.HasPrefix(a, "-") && path == "":
			path = a
		default:
			return usageError{fmt.Errorf("watch: unknown arg: %s", a)}
		}
	}
	logger.Init(stderr, verbose, debug)

	resultsDir := filepath.Join(resolveRunsDir(repo), agent.ResultsSubdir)

	pinned := path
	if pinned == "" && id != "" {
		stem := strings.TrimSuffix(id, agent.TranscriptExt)
		pinned = filepath.Join(resultsDir, stem+agent.TranscriptExt)
	}

	w := &watcher{
		out:        stdout,
		status:     stderr,
		resultsDir: resultsDir,
		pinned:     pinned,
		isTTY:      console.IsTerminal(stdout),
	}
	return w.run(ctx)
}

// resolveRunsDir loads the layered config to find the runs directory the loop
// writes to, falling back to the default when config can't be read — watch is a
// read-only inspector, so a broken config should degrade, not error out.
func resolveRunsDir(repo string) string {
	repoRoot, _ := config.ResolveRepoRoot(repo, os.Getenv("TRAU_REPO_ROOT"), config.GitToplevel)
	userEnv := ""
	if home, err := os.UserHomeDir(); err == nil {
		userEnv = config.ProjectConfigPath(home)
	}
	cfg, err := config.LoadLayered(config.ProjectConfigPath(repoRoot), userEnv, config.LocalConfigPath(), "")
	if err != nil {
		logger.Verbosef("watch: config load failed, using default runs dir: %v", err)
		return ".trau/runs"
	}
	return cfg.RunsDir
}

// watcher reconstructs an agent's live screen from its raw PTY transcript and
// renders it: an in-place repaint on a TTY, throttled appended snapshots when
// piped. It holds a single virtual terminal that is replaced whenever the
// followed transcript changes (a new phase) or is truncated in place.
type watcher struct {
	out        io.Writer
	status     io.Writer
	resultsDir string
	pinned     string // explicit target; empty means follow the newest transcript
	isTTY      bool

	screen    *vterm.Screen
	cols      int
	rows      int
	curPath   string
	offset    int64
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
		w.tick()
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (w *watcher) tick() {
	target := w.pinned
	if target == "" {
		target = newestTranscript(w.resultsDir)
	}
	if target != "" {
		if _, err := os.Stat(target); err != nil {
			target = ""
		}
	}
	if target == "" {
		w.announceWaiting()
		return
	}
	w.waiting = false
	if target != w.curPath {
		w.switchTo(target)
	}
	if w.readDelta() {
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

// switchTo points the watcher at a transcript with a fresh screen, read from the
// top so the current frame reconstructs in full.
func (w *watcher) switchTo(path string) {
	if w.screen != nil {
		w.screen.Close()
	}
	w.cols, w.rows, _ = agent.ReadSize(path)
	w.screen = vterm.New(w.cols, w.rows)
	w.curPath = path
	w.offset = 0
	w.lastFrame = ""
	if !w.isTTY {
		_, _ = fmt.Fprintf(w.out, "── %s ──\n", transcriptStem(path))
	}
}

// readDelta feeds the bytes appended since the last read into the emulator,
// restarting the screen if the file shrank (an in-place O_TRUNC reuse). It
// reports whether new bytes arrived.
func (w *watcher) readDelta() bool {
	f, err := os.Open(w.curPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() < w.offset {
		w.offset = 0
		w.screen.Close()
		w.screen = vterm.New(w.cols, w.rows)
		w.lastFrame = ""
	}
	if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
		return false
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return false
	}
	w.offset += int64(len(data))
	w.screen.Write(data)
	return true
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
	b.WriteString("\033[K" + "\033[2m▶ watch " + transcriptStem(w.curPath) + " · ctrl-c to stop\033[0m\r\n")
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

// newestTranscript returns the most-recently-modified .pty.log under dir, or ""
// when none exists yet (the loop hasn't started a phase, or dir is absent).
func newestTranscript(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), agent.TranscriptExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestMod) {
			newestMod = info.ModTime()
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

func transcriptStem(path string) string {
	return strings.TrimSuffix(filepath.Base(path), agent.TranscriptExt)
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
