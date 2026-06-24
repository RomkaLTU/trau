// Package budget enforces spend ceilings over the normalized token/cost ledger.
//
// Trau already measures per-call tokens and USD across providers (internal/tokens);
// this package turns that measurement strength into a governance control. Limits
// holds per-ticket and per-day caps; Check compares accumulated Spend against them
// and reports the first cap crossed as a Breach, which the pipeline uses to halt
// before the next agent call and quarantine the ticket with a clear cost-overrun
// note — instead of silently running up the bill.
//
// The zero Limits enforces nothing (every cap unset = unlimited), so adding caps is
// backward-compatible: a config with no MAX_* knobs behaves exactly as before.
package budget

import (
	"fmt"
	"strconv"
	"strings"
)

// Limits are the configured spend ceilings. A zero value for any field means "no
// cap" for that dimension, so the zero Limits enforces nothing.
type Limits struct {
	TicketUSD    float64 `json:"ticket_usd"`
	TicketTokens int     `json:"ticket_tokens"`
	DailyUSD     float64 `json:"daily_usd"`
	DailyTokens  int     `json:"daily_tokens"`
}

// Enabled reports whether any cap is configured.
func (l Limits) Enabled() bool {
	return l.TicketUSD > 0 || l.TicketTokens > 0 || l.DailyUSD > 0 || l.DailyTokens > 0
}

// Spend is an accumulated (tokens, cost) figure for one scope. Metered is false
// when some calls in the sum reported tokens but no per-call USD (a kimi/codex
// subscription phase), so Cost is then a LOWER BOUND, not a measured total. A USD
// cap is only breached when even that lower bound crosses it, so unmetered spend
// never produces a false breach — the token cap is the reliable lever there.
type Spend struct {
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
	Metered bool    `json:"cost_measured"`
}

// Scope is the window a cap governs: a single ticket or the whole calendar day.
type Scope string

const (
	ScopeTicket Scope = "ticket"
	ScopeDaily  Scope = "daily"
)

// Metric is the dimension a cap measures: dollars or raw tokens.
type Metric string

const (
	MetricUSD    Metric = "usd"
	MetricTokens Metric = "tokens"
)

// Breach describes the first cap a Check found crossed.
type Breach struct {
	Scope  Scope
	Metric Metric
	// Limit is the configured ceiling (dollars for MetricUSD, a token count for
	// MetricTokens); Spent is the measured spend that crossed it.
	Limit float64
	Spent float64
}

// Reason renders the breach as a one-line, human cost-overrun note suitable for
// the quarantine comment and the console.
func (b Breach) Reason() string {
	who, cap := "this ticket's", "per-ticket"
	if b.Scope == ScopeDaily {
		who, cap = "today's", "daily"
	}
	if b.Metric == MetricUSD {
		return fmt.Sprintf("%s estimated cost $%s reached the %s budget of $%s",
			who, money(b.Spent), cap, money(b.Limit))
	}
	return fmt.Sprintf("%s token usage (%s) reached the %s budget of %s tokens",
		who, commaInt(int(b.Spent)), cap, commaInt(int(b.Limit)))
}

// Check returns the first cap crossed by the given ticket-scoped and day-scoped
// spend, or ok=false when every configured cap is still satisfied (and when no cap
// is configured at all). Ticket caps are tested before daily caps so a ticket's own
// overrun is attributed to it. A cap trips at-or-over its ceiling (>=), so the loop
// halts before the next call pushes further past it.
func (l Limits) Check(ticket, day Spend) (Breach, bool) {
	if b, ok := l.checkTicket(ticket); ok {
		return b, true
	}
	return l.CheckDaily(day)
}

func (l Limits) checkTicket(ticket Spend) (Breach, bool) {
	if l.TicketUSD > 0 && ticket.Cost >= l.TicketUSD {
		return Breach{ScopeTicket, MetricUSD, l.TicketUSD, ticket.Cost}, true
	}
	if l.TicketTokens > 0 && ticket.Tokens >= l.TicketTokens {
		return Breach{ScopeTicket, MetricTokens, float64(l.TicketTokens), float64(ticket.Tokens)}, true
	}
	return Breach{}, false
}

// CheckDaily reports whether the day's accumulated spend has reached a daily cap.
// It is the loop-level gate: when a day is already over budget the run stops
// cleanly rather than quarantining every remaining ticket against the same ceiling.
func (l Limits) CheckDaily(day Spend) (Breach, bool) {
	if l.DailyUSD > 0 && day.Cost >= l.DailyUSD {
		return Breach{ScopeDaily, MetricUSD, l.DailyUSD, day.Cost}, true
	}
	if l.DailyTokens > 0 && day.Tokens >= l.DailyTokens {
		return Breach{ScopeDaily, MetricTokens, float64(l.DailyTokens), float64(day.Tokens)}, true
	}
	return Breach{}, false
}

// Report is the budget section of `trau --status`: the configured caps plus the
// day's spend so far, so an operator can see headroom at a glance.
type Report struct {
	Date   string `json:"date"`
	Limits Limits `json:"limits"`
	Today  Spend  `json:"today"`
}

// Summary renders the caps as a compact one-liner, omitting unset dimensions, e.g.
// "budget caps: ticket ≤ $0.50 · daily ≤ $5 / 2,000,000 tok". Empty when no cap is
// set.
func (l Limits) Summary() string {
	var parts []string
	if t := capPair(l.TicketUSD, l.TicketTokens); t != "" {
		parts = append(parts, "ticket ≤ "+t)
	}
	if d := capPair(l.DailyUSD, l.DailyTokens); d != "" {
		parts = append(parts, "daily ≤ "+d)
	}
	if len(parts) == 0 {
		return ""
	}
	return "budget caps: " + strings.Join(parts, " · ")
}

// Summary renders the caps line followed by the day's spend so far, e.g.
// "budget caps: daily ≤ $5 · today: $1.20 / 480,000 tok". Empty when no cap is set.
func (r Report) Summary() string {
	s := r.Limits.Summary()
	if s == "" {
		return ""
	}
	return s + " · today: " + spendStr(r.Today)
}

func capPair(usd float64, tok int) string {
	var p []string
	if usd > 0 {
		p = append(p, "$"+money(usd))
	}
	if tok > 0 {
		p = append(p, commaInt(tok)+" tok")
	}
	return strings.Join(p, " / ")
}

func spendStr(s Spend) string {
	cost := "$" + money(s.Cost)
	switch {
	case s.Metered:
	case s.Cost == 0:
		cost = "$? unmetered"
	default:
		cost += "+"
	}
	return cost + " / " + commaInt(s.Tokens) + " tok"
}

// money formats a USD amount as the shortest decimal (up to 4 places, trailing
// zeros trimmed): 0.5 → "0.5", 5 → "5", 0.0123 → "0.0123".
func money(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// commaInt renders an integer with thousands separators: 2000000 → "2,000,000".
func commaInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
