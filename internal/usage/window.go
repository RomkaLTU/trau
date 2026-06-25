// Package usage carries a provider's live rate-limit / usage window from a
// poller to the renderer.
//
// trau drives providers as CLI subagents and never sees their HTTP response
// headers, so each provider needs its own out-of-band probe and the data unit
// differs per provider (a utilization percentage for claude/codex, a prepaid
// balance for kimi, or nothing at all). Window is the one normalized shape every
// adapter maps into; it travels over the existing event seam as a [Window.Fields]
// map so internal/tui can render it without making any provider/network call.
package usage

import "time"

// EventKind is the structured-event kind that carries a Window. A poller emits one
// per probe onto the run's event.Log; the renderer consumes it in its event loop.
const EventKind = "usage_window"

// Window is the normalized provider usage window shown in the HUD. Providers
// disagree on both unit (a utilization percentage vs a prepaid USD balance) and
// reset encoding (ISO-8601, epoch seconds, Go duration); each adapter normalizes
// into this one shape. The zero value (Available=false) is the first-class "no
// window data" state — the HUD then shows token/cost totals with no bar and never
// fabricates a denominator.
type Window struct {
	// Available reports that a real window or balance was resolved. False means
	// degrade to totals-only.
	Available bool
	// Provider is the backend this window describes (claude/codex/kimi).
	Provider string
	// Source is the mechanism that produced it, for diagnostics: "oauth",
	// "app-server", "rollout", "balance", or "pty".
	Source string
	// Label names the binding window the bar represents: "5h", "7d", "weekly", or
	// "balance".
	Label string

	// Utilization is the binding window's consumption, 0..100. Valid only when
	// HasUtilization; that flag distinguishes a real 0% from "unknown".
	Utilization    float64
	HasUtilization bool

	// ResetAt is when the binding window rolls over. Advisory (some providers only
	// approximate it). Valid only when HasReset.
	ResetAt  time.Time
	HasReset bool

	// BalanceUSD is a prepaid balance (kimi with a platform key) when no window
	// concept exists. Valid only when HasBalance.
	BalanceUSD float64
	HasBalance bool
}

// field keys for the event seam. Kept here so the producer and the renderer agree
// on one spelling and optional fields are simply absent rather than zero-valued.
const (
	fAvailable   = "available"
	fProvider    = "provider"
	fSource      = "source"
	fLabel       = "label"
	fUtilization = "utilization"
	fResetAt     = "reset_at"
	fBalanceUSD  = "balance_usd"
)

// Fields encodes the window for an event.Log emit. Optional members are omitted
// (not zero-filled) so the decoder can tell "unset" from a real zero. ResetAt is
// carried as RFC-3339 text, the one reset encoding both sides share.
func (w Window) Fields() map[string]any {
	f := map[string]any{
		fAvailable: w.Available,
		fProvider:  w.Provider,
		fSource:    w.Source,
		fLabel:     w.Label,
	}
	if w.HasUtilization {
		f[fUtilization] = w.Utilization
	}
	if w.HasReset {
		f[fResetAt] = w.ResetAt.Format(time.RFC3339)
	}
	if w.HasBalance {
		f[fBalanceUSD] = w.BalanceUSD
	}
	return f
}

// WindowFromFields reconstructs a Window from an event's Fields map. It is
// tolerant of the live in-memory path (Go-typed values) and a JSON round-trip
// (float64 numbers, string times); a missing or unparseable optional simply
// leaves its Has* flag false.
func WindowFromFields(f map[string]any) Window {
	w := Window{
		Available: asBool(f[fAvailable]),
		Provider:  asString(f[fProvider]),
		Source:    asString(f[fSource]),
		Label:     asString(f[fLabel]),
	}
	if v, ok := asFloat(f[fUtilization]); ok {
		w.Utilization, w.HasUtilization = v, true
	}
	if s := asString(f[fResetAt]); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			w.ResetAt, w.HasReset = t, true
		}
	}
	if v, ok := asFloat(f[fBalanceUSD]); ok {
		w.BalanceUSD, w.HasBalance = v, true
	}
	return w
}

// Remaining returns the time until the window resets relative to now, and whether
// a positive remaining is known. A past or unknown reset yields ok=false.
func (w Window) Remaining(now time.Time) (time.Duration, bool) {
	if !w.HasReset {
		return 0, false
	}
	d := w.ResetAt.Sub(now)
	if d <= 0 {
		return 0, false
	}
	return d, true
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
