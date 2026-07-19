package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func restartServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := New("2.1.0", "127.0.0.1", "", nil, false, testStores(t))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

// TestRestartAcknowledgesThenSignals checks the caller learns the outgoing
// version over the connection it is about to lose, and that the signal reaches
// the serve command only after the response is on the wire.
func TestRestartAcknowledgesThenSignals(t *testing.T) {
	s, ts := restartServer(t)
	signalled := make(chan struct{})
	s.EnableRestart(func() { close(signalled) })

	res, err := http.Post(ts.URL+APIPrefix+"/hub/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
	}
	var ack RestartAck
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !ack.Restarting || ack.Version != "2.1.0" {
		t.Fatalf("ack = %+v, want the running version and restarting=true", ack)
	}

	<-signalled
}

// TestRestartSignalsOnlyOnce checks a second POST arriving while the hub drains
// is acknowledged without spawning a second successor — two hubs racing for the
// port is worse than one restart that a client asked for twice.
func TestRestartSignalsOnlyOnce(t *testing.T) {
	s, ts := restartServer(t)
	var mu sync.Mutex
	calls := 0
	s.EnableRestart(func() {
		mu.Lock()
		defer mu.Unlock()
		calls++
	})

	for range 3 {
		res, err := http.Post(ts.URL+APIPrefix+"/hub/restart", "application/json", nil)
		if err != nil {
			t.Fatalf("POST restart: %v", err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusAccepted)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("restart signalled %d times, want exactly 1", calls)
	}
}

// TestRestartWithoutSpawnerIsUnavailable checks a hub with no successor to spawn
// says so rather than acknowledging a restart that will never happen.
func TestRestartWithoutSpawnerIsUnavailable(t *testing.T) {
	_, ts := restartServer(t)

	res, err := http.Post(ts.URL+APIPrefix+"/hub/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusServiceUnavailable)
	}
}

// TestRestartRejectsGET keeps the endpoint out of reach of a link or a prefetch.
func TestRestartRejectsGET(t *testing.T) {
	s, ts := restartServer(t)
	s.EnableRestart(func() { t.Error("GET triggered a restart") })

	res, err := http.Get(ts.URL + APIPrefix + "/hub/restart")
	if err != nil {
		t.Fatalf("GET restart: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusMethodNotAllowed)
	}
}

// TestRestartRequiresTokenOnExposedBind checks the restart endpoint sits behind
// the same bearer-token auth as every other control endpoint.
func TestRestartRequiresTokenOnExposedBind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := New("2.1.0", "0.0.0.0", "s3cret", nil, false, testStores(t))
	s.EnableRestart(func() { t.Error("unauthenticated POST triggered a restart") })
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+APIPrefix+"/hub/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("POST restart: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUnauthorized)
	}
}
