package hubtokens

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
	"github.com/RomkaLTU/trau/internal/tokens"
)

// fakeHub records the token calls and anomalies it receives and returns configured
// aggregate reads. It can fail a number of leading appends with a genuine unreachable
// error so the retry path is exercised.
type fakeHub struct {
	mu        sync.Mutex
	batches   [][]hubclient.TokenCall
	anomalies map[string][]hubclient.Anomaly
	total     hubclient.Spend
	day       hubclient.Spend
	fails     int
	unreach   error
}

func (f *fakeHub) AppendTokenCalls(_ context.Context, _ string, calls []hubclient.TokenCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return f.unreach
	}
	f.batches = append(f.batches, append([]hubclient.TokenCall(nil), calls...))
	return nil
}

func (f *fakeHub) TokenTotal(context.Context, string, string) (hubclient.Spend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total, nil
}

func (f *fakeHub) TokenDayTotal(context.Context, string, string) (hubclient.Spend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.day, nil
}

func (f *fakeHub) RecordAnomalies(_ context.Context, _, ticket string, anomalies []hubclient.Anomaly) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.anomalies == nil {
		f.anomalies = map[string][]hubclient.Anomaly{}
	}
	f.anomalies[ticket] = anomalies
	return nil
}

func (f *fakeHub) received() []hubclient.TokenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hubclient.TokenCall
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func usd(v float64) *float64 { return &v }

func fixedClock(s *Sink) {
	s.now = func() time.Time { return time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC) }
}

// unreachableErr produces a real hubclient transport error by dialing a dead port,
// so IsUnreachable recognizes it — the same trick the event sink's test uses.
func unreachableErr(t *testing.T) error {
	t.Helper()
	err := hubclient.New("http://127.0.0.1:1", "").AppendTokenCalls(context.Background(), "repo", nil)
	if err == nil || !hubclient.IsUnreachable(err) {
		t.Fatalf("expected an unreachable error, got %v", err)
	}
	return err
}

func TestSinkBuffersAndPostsInOrder(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("COD-1")

	s.Append("build", tokens.Record{Input: 100, Output: 50, CostUSD: usd(0.10)})
	s.Append("build", tokens.Record{}) // zero total — dropped
	s.Append("verify", tokens.Record{Input: 200, Output: 100, CostUSD: usd(0.20)})
	s.flush()

	got := fake.received()
	if len(got) != 2 {
		t.Fatalf("received %d calls, want 2 (zero-total dropped)", len(got))
	}
	if got[0].Phase != "build" || got[0].Ticket != "COD-1" || got[0].Total != 150 {
		t.Errorf("call 0 = %+v, want the build call bucketed at COD-1", got[0])
	}
	if got[1].Phase != "verify" || got[1].Total != 300 {
		t.Errorf("call 1 = %+v, want the verify call after it in order", got[1])
	}
}

func TestSinkTotalFlushesThenReads(t *testing.T) {
	fake := &fakeHub{total: hubclient.Spend{Tokens: 150, Cost: 0.10, Metered: true}}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("COD-1")
	s.Append("build", tokens.Record{Input: 100, Output: 50, CostUSD: usd(0.10)})

	toks, cost, metered := s.Total("COD-1")
	if toks != 150 || cost != 0.10 || !metered {
		t.Errorf("Total = (%d, %v, %v), want the hub's (150, 0.10, true)", toks, cost, metered)
	}
	if len(fake.received()) != 1 {
		t.Errorf("received %d calls, want the buffered call flushed before the read", len(fake.received()))
	}
}

func TestSinkDayTotalFlushesThenReads(t *testing.T) {
	fake := &fakeHub{day: hubclient.Spend{Tokens: 420, Cost: 0.42, Metered: false}}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.Append("pick", tokens.Record{Input: 20, Output: 10})

	toks, cost, metered := s.DayTotal("2026-07-12")
	if toks != 420 || cost != 0.42 || metered {
		t.Errorf("DayTotal = (%d, %v, %v), want the hub's (420, 0.42, false)", toks, cost, metered)
	}
	if len(fake.received()) != 1 {
		t.Errorf("received %d calls, want the buffered call flushed before the read", len(fake.received()))
	}
}

func TestSinkSessionTotalIsInMemory(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("COD-1")
	s.Append("build", tokens.Record{Input: 100, Output: 50, CostUSD: usd(0.10)})
	s.Append("verify", tokens.Record{Input: 200, Output: 100, CostUSD: usd(0.25)})

	toks, cost, metered := s.SessionTotal("COD-1")
	if toks != 450 || cost != 0.35 || !metered {
		t.Errorf("SessionTotal = (%d, %v, %v), want (450, 0.35, true)", toks, cost, metered)
	}
	if _, _, m := s.SessionTotal("COD-1"); !m {
		t.Error("session metered flipped, want stable")
	}
	if tk, _, _ := s.SessionTotal("COD-404"); tk != 0 {
		t.Error("unknown ticket session total nonzero, want 0")
	}
}

func TestSinkFlagRecordsAnomalies(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("COD-9")
	s.Append("cleanup", tokens.Record{Output: 120_000, Turns: 8, CostUSD: usd(6.50)})

	anomalies := s.Flag("COD-9")
	if len(anomalies) != 1 || anomalies[0].Phase != "cleanup" {
		t.Fatalf("Flag = %+v, want the cleanup trip", anomalies)
	}
	recorded := fake.anomalies["COD-9"]
	if len(recorded) != 1 || recorded[0].Phase != "cleanup" || recorded[0].Cost != 6.5 || len(recorded[0].Reasons) == 0 {
		t.Errorf("recorded anomalies = %+v, want the cleanup trip posted to the hub", recorded)
	}

	// A quiet ticket flags nothing and records nothing.
	s.SetTicket("COD-10")
	s.Append("build", tokens.Record{Output: 100, Turns: 2, CostUSD: usd(0.10)})
	if got := s.Flag("COD-10"); got != nil {
		t.Errorf("quiet Flag = %+v, want nil", got)
	}
	if _, ok := fake.anomalies["COD-10"]; ok {
		t.Error("quiet ticket recorded anomalies, want none")
	}
}

func TestSinkDropsOldestOverCap(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("t")
	// callBytes = len("t")+len(ts=19)+len(phase=1)+callOverhead(96) = 117 each; a
	// 250-byte cap holds two and drops the oldest when the third arrives.
	s.maxBytes = 250
	s.Append("1", tokens.Record{Input: 1})
	s.Append("2", tokens.Record{Input: 2})
	s.Append("3", tokens.Record{Input: 3})
	s.flush()

	got := fake.received()
	if len(got) != 2 || got[0].Phase != "2" || got[1].Phase != "3" {
		t.Fatalf("received %+v, want the last two calls (oldest dropped over cap)", got)
	}
}

func TestSinkRetriesUnreachableThenFlushes(t *testing.T) {
	fake := &fakeHub{fails: 2, unreach: unreachableErr(t)}
	s := newSink(fake, "repo", 0, time.Second)
	fixedClock(s)
	s.sleep = func(time.Duration) {}
	s.SetTicket("COD-1")
	s.Append("build", tokens.Record{Input: 100, Output: 50, CostUSD: usd(0.10)})
	s.flush()

	if len(fake.received()) != 1 {
		t.Fatalf("received %d calls, want 1 after the hub recovered", len(fake.received()))
	}
}

func TestSinkCloseFlushesTail(t *testing.T) {
	fake := &fakeHub{}
	s := New(fake, "repo", 0, 0)
	s.SetTicket("COD-1")
	s.Append("commit", tokens.Record{Input: 50, Output: 20, CostUSD: usd(0.01)})
	s.Close()

	if len(fake.received()) != 1 {
		t.Fatalf("received %d calls, want the tail flushed on Close", len(fake.received()))
	}
}

// TestSinkStampsRoutingOnEveryCall covers the ledger's routing columns on the
// child's write path: each call carries the effort and duration the backend
// reported plus the run's config fingerprint.
func TestSinkStampsRoutingOnEveryCall(t *testing.T) {
	fake := &fakeHub{}
	s := newSink(fake, "repo", 0, 0)
	fixedClock(s)
	s.SetTicket("COD-1")
	s.SetConfigHash("9f1c2a3b")

	s.Append("verify", tokens.Record{Input: 100, Output: 50, Effort: "high", Duration: 42 * time.Second})
	s.Append("commit", tokens.Record{Input: 10, Output: 5})
	s.flush()

	got := fake.received()
	if len(got) != 2 {
		t.Fatalf("received %d calls, want 2", len(got))
	}
	if got[0].Effort != "high" || got[0].DurationMS != 42_000 || got[0].ConfigHash != "9f1c2a3b" {
		t.Errorf("call 0 = %+v, want effort high, 42000ms, hash 9f1c2a3b", got[0])
	}
	if got[1].Effort != "" || got[1].DurationMS != 0 || got[1].ConfigHash != "9f1c2a3b" {
		t.Errorf("call 1 = %+v, want the fingerprint stamped even with no effort reported", got[1])
	}
}
