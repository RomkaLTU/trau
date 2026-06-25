// Package probe resolves a provider's live usage window out-of-band and pushes it
// onto the run's event log for the HUD to render.
//
// trau drives providers as CLI subagents and never sees their HTTP headers, so
// there is no uniform source: claude exposes a percentage over an OAuth HTTP
// endpoint, codex a percentage over a JSON-RPC app-server, and kimi nothing but a
// prepaid balance (or a screen-scraped panel). Each provider gets its own Prober;
// every probe is metadata-only — it must never consume model tokens or run an
// agent turn — and a [Poller] calls the configured one on a slow cadence, emitting
// a [usage.Window] event each time.
package probe

import (
	"context"
	"time"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/usage"
)

// minInterval is the floor on poll cadence. The probe endpoints are cheap and
// metadata-only, but they are also undocumented/version-sensitive, so we poll
// gently (the research note recommends >= ~180s).
const minInterval = 180 * time.Second

// Prober resolves the current usage window for one provider. Implementations MUST
// be metadata-only and are called at most once per poll interval. Any error,
// timeout, or missing data returns the zero usage.Window (Available=false) so the
// HUD degrades to totals-only rather than guessing a denominator.
type Prober interface {
	Probe(ctx context.Context) usage.Window
}

// Options configures probing, mapped from config at the composition root so this
// package need not import internal/config.
type Options struct {
	Provider   string        // configured backend: claude | codex | kimi
	Enabled    bool          // master switch (config USAGE_WINDOW); false => no polling
	PTY        bool          // allow the pseudo-terminal /usage fallback (config USAGE_WINDOW_PTY)
	Interval   time.Duration // poll cadence; clamped up to minInterval
	ClaudeBin  string
	CodexBin   string
	KimiBin    string
	KimiAPIKey string // Moonshot platform key for the balance probe; empty => no balance
}

// New returns the Prober for the configured provider, or nil when none applies —
// the feature is off, the provider is unknown, or the provider exposes no window
// and the PTY fallback is disabled. A nil Prober keeps the HUD totals-only.
func New(o Options) Prober {
	if !o.Enabled {
		return nil
	}
	switch o.Provider {
	case "claude":
		return &claudeProber{bin: o.ClaudeBin}
	case "codex":
		return &codexProber{bin: o.CodexBin}
	case "kimi":
		if o.KimiAPIKey != "" {
			return &kimiBalanceProber{apiKey: o.KimiAPIKey}
		}
		if o.PTY {
			return &ptyProber{provider: "kimi", bin: o.KimiBin}
		}
		return nil
	}
	return nil
}

// NewPoller builds the poller for o, or nil when no prober applies or there is no
// log to emit onto. The caller runs Poller.Run in a goroutine for the lifetime of
// a loop; emitted windows reach the renderer through the log's human hook.
func NewPoller(o Options, log *event.Log) *Poller {
	pr := New(o)
	if pr == nil || log == nil {
		return nil
	}
	return &Poller{log: log, prober: pr, interval: o.Interval}
}

// Poller probes a single provider on a fixed cadence and emits each result as a
// usage_window event.
type Poller struct {
	log      *event.Log
	prober   Prober
	interval time.Duration
}

// Run probes once immediately, then every interval (>= minInterval) until ctx is
// done, emitting one usage_window event per probe. It is safe to call on a nil
// receiver, so callers can `go NewPoller(...).Run(ctx)` unconditionally.
func (p *Poller) Run(ctx context.Context) {
	if p == nil || p.prober == nil || p.log == nil {
		return
	}
	interval := p.interval
	if interval < minInterval {
		interval = minInterval
	}
	p.emit(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.emit(ctx)
		}
	}
}

func (p *Poller) emit(ctx context.Context) {
	w := p.prober.Probe(ctx)
	p.log.Emit(usage.EventKind, "", "", w.Fields())
}
