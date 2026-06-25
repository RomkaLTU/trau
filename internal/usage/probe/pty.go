package probe

import (
	"context"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/creack/pty"

	"github.com/RomkaLTU/trau/internal/usage"
)

// ptyProber drives a provider's interactive CLI in a pseudo-terminal, sends the
// /usage slash command, and scrapes the rendered panel. It is the only route to
// usage for Kimi-Code *subscription* users (no endpoint, no file, print-mode
// rejects /usage) and a last resort otherwise. It is strictly inferior to the
// structured probes — the panel layout is terminal-width- and version-dependent —
// so it is opt-in (config USAGE_WINDOW_PTY) and always fails closed.
//
// /usage is handled client-side, so launching the TUI and sending it makes no
// model call; the probe stays metadata-only as long as no prompt is ever sent.
type ptyProber struct {
	provider string
	bin      string
}

func (p *ptyProber) Probe(ctx context.Context) usage.Window {
	if p.bin == "" {
		return usage.Window{}
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.bin)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 160})
	if err != nil {
		return usage.Window{}
	}
	defer func() {
		_ = f.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Let the TUI boot, ask for usage, then collect the rendered frames.
	if !sleepCtx(ctx, 3*time.Second) {
		return usage.Window{}
	}
	_, _ = io.WriteString(f, "/usage\r")

	out := drain(ctx, f, 8*time.Second)
	return parseUsagePanel(p.provider, stripANSI(out))
}

// drain reads from r until the deadline elapses or ctx is done, returning whatever
// was captured. A pty read blocks, so the read runs in a goroutine and the timer
// bounds it; the deferred Close on the pty unblocks the goroutine on return.
func drain(ctx context.Context, r io.Reader, d time.Duration) []byte {
	type chunk struct {
		b   []byte
		err error
	}
	ch := make(chan chunk, 1)
	var buf []byte
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, tmp[:n])
				ch <- chunk{b: cp}
			}
			if err != nil {
				ch <- chunk{err: err}
				return
			}
		}
	}()
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return buf
		case <-deadline.C:
			return buf
		case c := <-ch:
			if c.err != nil {
				return buf
			}
			buf = append(buf, c.b...)
		}
	}
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// stripANSI removes terminal escape sequences so the panel can be matched as plain
// text.
func stripANSI(b []byte) string {
	return ansiRe.ReplaceAllString(string(b), "")
}

var (
	pctRe   = regexp.MustCompile(`(\d{1,3})\s*%`)
	resetRe = regexp.MustCompile(`(?i)reset[^0-9]*?(?:(\d+)\s*h)?[^0-9]*?(?:(\d+)\s*m)`)
)

// parseUsagePanel extracts a utilization percentage (and, when present, a relative
// reset hint) from the scraped /usage panel. It returns Available only when a
// percentage is found — never a fabricated denominator.
func parseUsagePanel(provider, text string) usage.Window {
	m := pctRe.FindStringSubmatch(text)
	if m == nil {
		return usage.Window{}
	}
	pct, err := strconv.Atoi(m[1])
	if err != nil || pct > 100 {
		return usage.Window{}
	}
	w := usage.Window{
		Available:      true,
		Provider:       provider,
		Source:         "pty",
		Label:          "usage",
		Utilization:    float64(pct),
		HasUtilization: true,
	}
	if r := resetRe.FindStringSubmatch(text); r != nil {
		var d time.Duration
		if r[1] != "" {
			if h, err := strconv.Atoi(r[1]); err == nil {
				d += time.Duration(h) * time.Hour
			}
		}
		if r[2] != "" {
			if mins, err := strconv.Atoi(r[2]); err == nil {
				d += time.Duration(mins) * time.Minute
			}
		}
		if d > 0 {
			w.ResetAt, w.HasReset = time.Now().Add(d), true
		}
	}
	return w
}

// sleepCtx sleeps for d, returning false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
