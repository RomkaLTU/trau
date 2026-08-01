package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func hubStopServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := New("2.1.0", "127.0.0.1", "", nil, false, testStores(t))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// TestStopAcknowledgesThenSignals checks the caller learns the outgoing version
// over the connection the shutdown is about to close, and that the signal
// reaches the serve command only once the response is on the wire.
func TestStopAcknowledgesThenSignals(t *testing.T) {
	s, ts := hubStopServer(t)
	signalled := make(chan struct{})
	s.EnableStop(func() { close(signalled) })

	res, err := http.Post(ts.URL+APIPrefix+"/hub/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}
	var ack StopAck
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !ack.Stopping || ack.Version != "2.1.0" {
		t.Fatalf("ack = %+v, want the running version and stopping=true", ack)
	}

	<-signalled
}

// TestStopSignalsOnlyOnce checks a second POST arriving while the hub drains is
// acknowledged rather than draining it twice — a caller that asked twice gets an
// answer, and the hub still goes down once.
func TestStopSignalsOnlyOnce(t *testing.T) {
	s, ts := hubStopServer(t)
	var mu sync.Mutex
	calls := 0
	s.EnableStop(func() {
		mu.Lock()
		defer mu.Unlock()
		calls++
	})

	for range 3 {
		res, err := http.Post(ts.URL+APIPrefix+"/hub/stop", "application/json", nil)
		if err != nil {
			t.Fatalf("POST stop: %v", err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("stop signalled %d times, want exactly 1", calls)
	}
}

// TestStopWithoutAHookIsUnavailable checks a hub embedded in something other than
// `trau serve` says so rather than acknowledging a stop that will never happen.
func TestStopWithoutAHookIsUnavailable(t *testing.T) {
	_, ts := hubStopServer(t)

	res, err := http.Post(ts.URL+APIPrefix+"/hub/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestStopRejectsGET keeps the endpoint out of reach of a link or a prefetch.
func TestStopRejectsGET(t *testing.T) {
	s, ts := hubStopServer(t)
	s.EnableStop(func() { t.Error("GET stopped the hub") })

	res, err := http.Get(ts.URL + APIPrefix + "/hub/stop")
	if err != nil {
		t.Fatalf("GET stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := res.Header.Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want %q", got, http.MethodPost)
	}
}

// TestStopRequiresTokenOnExposedBind checks the stop endpoint sits behind the
// same bearer-token auth as every other control endpoint.
func TestStopRequiresTokenOnExposedBind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := New("2.1.0", "0.0.0.0", "s3cret", nil, false, testStores(t))
	s.EnableStop(func() { t.Error("unauthenticated POST stopped the hub") })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+APIPrefix+"/hub/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}
}

// TestStopRefusesOnASupervisedHub checks the hub launchd owns says so instead of
// exiting into a KeepAlive respawn, which would not leave it stopped.
func TestStopRefusesOnASupervisedHub(t *testing.T) {
	s, ts := hubStopServer(t)
	s.EnableStop(func() { t.Error("a supervised hub exited into its own respawn") })
	s.EnableSupervision(func(string) error { return nil })

	res, err := http.Post(ts.URL+APIPrefix+"/hub/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST stop: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
	}
}
