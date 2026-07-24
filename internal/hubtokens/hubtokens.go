// Package hubtokens is the loop child's hub-backed token/cost sink. It satisfies the
// agent and pipeline token seams by posting every provider call's usage to the serve
// hub over HTTP (ADR 0008): the child stops appending per-run tokens.jsonl /
// anomalies.jsonl files and sends token calls to the hub, which appends them to the
// authoritative token_calls table.
//
// Append never blocks the loop. Calls queue in a byte-bounded in-memory buffer that a
// background flusher drains, batching bursts into one POST and retrying an unreachable
// hub with backoff — the same non-blocking, drop-oldest contract as the event sink.
// The aggregate reads the pipeline needs for budget enforcement (Total, DayTotal)
// flush the buffer first, then query the hub, so a mid-loop budget check sees every
// call this session appended. A sustained hub outage surfaces as a blameless pause
// through the checkpoint writer, which shares the same hub; the token sink only holds
// calls until the hub returns, dropping the oldest when the buffer fills.
//
// Alongside the write buffer the sink keeps an in-memory session ledger of only what
// THIS process appended, per bucket, so SessionTotal and the end-of-session summary
// never report a resumed ticket's earlier-run dollars, and Flag detects anomalies
// from this session's spend without re-flagging spend loaded from an earlier run.
package hubtokens

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/logger"
	"github.com/RomkaLTU/trau/internal/tokens"
)

const (
	loopBucket = "_loop"

	baseBackoff = 200 * time.Millisecond
	maxBackoff  = 2 * time.Second
	retryTick   = 2 * time.Second

	defaultBufferBytes = 32 << 20
	defaultRetryWindow = 30 * time.Second

	callOverhead = 96
)

// hubAPI is the slice of hubclient the sink drives; *hubclient.Client satisfies it,
// and tests substitute a fake.
type hubAPI interface {
	AppendTokenCalls(ctx context.Context, repo string, calls []hubclient.TokenCall) error
	TokenTotal(ctx context.Context, repo, ticket string) (hubclient.Spend, error)
	TokenDayTotal(ctx context.Context, repo, date string) (hubclient.Spend, error)
	RecordAnomalies(ctx context.Context, repo, ticket string, anomalies []hubclient.Anomaly) error
}

// Sink is a hub-backed token/cost sink scoped to one repo.
type Sink struct {
	client   hubAPI
	repo     string
	maxBytes int
	window   time.Duration

	now   func() time.Time
	sleep func(time.Duration)

	mu         sync.Mutex
	configHash string
	bucket     string
	buf        []hubclient.TokenCall
	bytes      int
	session    map[string]*sessionSpend

	flushMu sync.Mutex

	notify chan struct{}
	done   chan struct{}
	closed chan struct{}
}

type sessionSpend struct {
	tokens  int
	cost    float64
	metered bool
	phases  map[string]*phaseSpend
	order   []string
}

type phaseSpend struct {
	output, turns int
	cost          float64
}

// New returns a Sink posting repo's token calls through client and starts its
// flusher. maxBytes caps the in-memory buffer (defaulted when non-positive); window
// bounds how long one flush retries an unreachable hub before backing off to the next
// cycle (defaulted when non-positive). Call Close to flush the tail before exit.
func New(client hubAPI, repo string, maxBytes int, window time.Duration) *Sink {
	s := newSink(client, repo, maxBytes, window)
	go s.run()
	return s
}

func newSink(client hubAPI, repo string, maxBytes int, window time.Duration) *Sink {
	if maxBytes <= 0 {
		maxBytes = defaultBufferBytes
	}
	if window <= 0 {
		window = defaultRetryWindow
	}
	return &Sink{
		client:   client,
		repo:     repo,
		maxBytes: maxBytes,
		window:   window,
		now:      time.Now,
		sleep:    time.Sleep,
		session:  map[string]*sessionSpend{},
		notify:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

// SetTicket points subsequent Appends at the given bucket. An empty id resets to the
// _loop bucket (the loop sets the current ticket on entry and clears it on exit).
func (s *Sink) SetTicket(id string) {
	s.mu.Lock()
	s.bucket = id
	s.mu.Unlock()
}

// SetConfigHash stamps subsequent Appends with the routing fingerprint the calls
// run under. It is stamped at construction and again at each run entry, where an
// ephemeral provider override may have moved it; an unset hash records the calls
// under the unknown cohort.
func (s *Sink) SetConfigHash(hash string) {
	s.mu.Lock()
	s.configHash = hash
	s.mu.Unlock()
}

// Append records one normalized call for a phase into the session ledger and queues
// it for the hub. Calls whose total is zero are dropped as uncaptured/empty — unless
// they carry live-captured skills, whose evidence must survive an unrecovered
// transcript so the phase reads as loaded, not "no data". It
// never blocks; when the buffer is over its byte cap the oldest queued calls are
// dropped to make room.
func (s *Sink) Append(phase string, rec tokens.Record) {
	total := rec.Input + rec.Output + rec.CacheRead + rec.CacheCreation
	if total <= 0 && len(rec.Skills) == 0 {
		return
	}
	s.mu.Lock()
	bucket := s.bucket
	if bucket == "" {
		bucket = loopBucket
	}
	s.recordSession(bucket, phase, rec, total)
	call := hubclient.TokenCall{
		Ticket:        bucket,
		TS:            s.now().Format("2006-01-02T15:04:05"),
		Phase:         phase,
		Input:         rec.Input,
		Output:        rec.Output,
		CacheRead:     rec.CacheRead,
		CacheCreation: rec.CacheCreation,
		Reasoning:     rec.Reasoning,
		Total:         total,
		CostUSD:       rec.CostUSD,
		Turns:         rec.Turns,
		IsError:       rec.IsError,
		Provider:      rec.Provider,
		Model:         rec.Model,
		Effort:        rec.Effort,
		Context:       rec.Context,
		DurationMS:    int(rec.Duration.Milliseconds()),
		ConfigHash:    s.configHash,
		Skills:        marshalSkills(rec.Skills),
	}
	s.buf = append(s.buf, call)
	s.bytes += callBytes(call)
	s.enforceCap()
	s.mu.Unlock()
	s.wake()
}

// recordSession folds one appended call into the in-memory session ledger.
// Caller holds s.mu.
func (s *Sink) recordSession(bucket, phase string, rec tokens.Record, total int) {
	sp := s.session[bucket]
	if sp == nil {
		sp = &sessionSpend{metered: true, phases: map[string]*phaseSpend{}}
		s.session[bucket] = sp
	}
	sp.tokens += total
	if rec.CostUSD != nil {
		sp.cost += *rec.CostUSD
	} else {
		sp.metered = false
	}
	ps := sp.phases[phase]
	if ps == nil {
		ps = &phaseSpend{}
		sp.phases[phase] = ps
		sp.order = append(sp.order, phase)
	}
	ps.output += rec.Output
	ps.turns += rec.Turns
	if rec.CostUSD != nil {
		ps.cost += *rec.CostUSD
	}
}

// Total sums a ticket's logged token + cost spend across all phases, read from the
// hub. It flushes the buffer first so a mid-loop read sees every call this session
// appended. An unreachable or erroring hub yields (0, 0, true) — never an error — so
// callers can print or budget-check it unconditionally.
func (s *Sink) Total(id string) (toks int, cost float64, metered bool) {
	s.flush()
	sp, err := s.client.TokenTotal(context.Background(), s.repo, id)
	if err != nil {
		return 0, 0, true
	}
	return sp.Tokens, sp.Cost, sp.Metered
}

// DayTotal sums the repo's spend for a local date (YYYY-MM-DD) across every bucket —
// the per-day window the budget caps enforce — read from the hub after flushing the
// buffer. An unreachable or erroring hub yields (0, 0, true).
func (s *Sink) DayTotal(date string) (toks int, cost float64, metered bool) {
	s.flush()
	sp, err := s.client.TokenDayTotal(context.Background(), s.repo, date)
	if err != nil {
		return 0, 0, true
	}
	return sp.Tokens, sp.Cost, sp.Metered
}

// SessionTotal sums the token + cost spend THIS process recorded for id from the
// in-memory ledger. Unlike Total it excludes spend loaded from an earlier run, so the
// end-of-session summary reflects what the session actually spent.
func (s *Sink) SessionTotal(id string) (toks int, cost float64, metered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.session[id]
	if sp == nil {
		return 0, 0, true
	}
	return sp.tokens, math.Round(sp.cost*100) / 100, sp.metered
}

// Flag detects this session's per-phase cost anomalies for id and records them
// through the hub, returning the trips. It sums each phase's output/turns/cost
// recorded by THIS process — spend loaded from an earlier run is never re-flagged on
// resume — and flags any phase over a soft threshold. A hub error is logged and
// swallowed: flagging never aborts the loop.
func (s *Sink) Flag(id string) []tokens.Anomaly {
	s.mu.Lock()
	sp := s.session[id]
	var phases []tokens.PhaseSpend
	if sp != nil {
		for _, phase := range sp.order {
			p := sp.phases[phase]
			phases = append(phases, tokens.PhaseSpend{Phase: phase, Output: p.output, Turns: p.turns, Cost: p.cost})
		}
	}
	s.mu.Unlock()

	anomalies := tokens.DetectAnomalies(phases)
	if len(anomalies) == 0 {
		return nil
	}
	ts := s.now().Format("2006-01-02T15:04:05")
	payload := make([]hubclient.Anomaly, len(anomalies))
	for i, a := range anomalies {
		payload[i] = hubclient.Anomaly{TS: ts, Phase: a.Phase, Output: a.Output, Turns: a.Turns, Cost: a.Cost, Reasons: a.Reasons}
	}
	if err := s.client.RecordAnomalies(context.Background(), s.repo, id, payload); err != nil {
		logger.Verbosef("record anomalies %s/%s: %v", s.repo, id, err)
	}
	return anomalies
}

// Close stops the flusher after a final attempt to flush the tail, so a run's last
// token calls reach the hub before the process exits. It is safe to call once.
func (s *Sink) Close() {
	close(s.done)
	<-s.closed
}

func (s *Sink) run() {
	defer close(s.closed)
	t := time.NewTicker(retryTick)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			s.flush()
			return
		case <-s.notify:
			s.flush()
		case <-t.C:
			s.flush()
		}
	}
}

// flush sends the whole buffer to the hub in one ordered batch. flushMu serializes it
// against the background flusher, so a read-triggered flush and a tick never post
// overlapping batches. On a lasting unreachable hub it puts the batch back at the
// front so the next cycle retries it ahead of anything newer, re-enforcing the cap.
func (s *Sink) flush() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.buf
	s.buf = nil
	s.bytes = 0
	s.mu.Unlock()

	if s.post(batch) {
		return
	}
	s.mu.Lock()
	s.buf = append(batch, s.buf...)
	s.bytes = totalBytes(s.buf)
	s.enforceCap()
	s.mu.Unlock()
}

// post flushes batch, retrying an unreachable hub with backoff until the window
// expires. It returns true when the batch is done with — flushed, or dropped because
// the hub rejected it (a non-connection error retrying cannot fix) — and false when
// the hub is still unreachable, so the caller keeps the batch.
func (s *Sink) post(batch []hubclient.TokenCall) bool {
	deadline := s.now().Add(s.window)
	backoff := baseBackoff
	for {
		err := s.client.AppendTokenCalls(context.Background(), s.repo, batch)
		if err == nil {
			return true
		}
		if !hubclient.IsUnreachable(err) {
			logger.Verbosef("append token calls %s: %v", s.repo, err)
			return true
		}
		if !s.now().Before(deadline) || s.stopping() {
			return false
		}
		s.sleep(backoff)
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func (s *Sink) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Sink) stopping() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Sink) enforceCap() {
	for s.bytes > s.maxBytes && len(s.buf) > 0 {
		s.bytes -= callBytes(s.buf[0])
		s.buf = s.buf[1:]
	}
}

func marshalSkills(skills []string) string {
	if len(skills) == 0 {
		return ""
	}
	b, err := json.Marshal(skills)
	if err != nil {
		return ""
	}
	return string(b)
}

func callBytes(c hubclient.TokenCall) int {
	return len(c.Ticket) + len(c.TS) + len(c.Phase) + len(c.Provider) + len(c.Model) +
		len(c.Effort) + len(c.ConfigHash) + len(c.Skills) + callOverhead
}

func totalBytes(calls []hubclient.TokenCall) int {
	n := 0
	for _, c := range calls {
		n += callBytes(c)
	}
	return n
}
