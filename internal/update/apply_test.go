package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
)

// applyHarness drives Apply without brew or a binary on disk: onDisk is what the
// probe reports and the test moves it to stand in for an upgrade landing.
type applyHarness struct {
	*Checker

	mu       sync.Mutex
	onDisk   string
	upgrades int

	restarted chan struct{}
}

func newApplyHarness(t *testing.T, running, onDisk, method string, upgrade func() ([]byte, error)) *applyHarness {
	t.Helper()

	h := &applyHarness{Checker: NewChecker(running), onDisk: onDisk, restarted: make(chan struct{}, 1)}
	h.endpoint = releaseServer(t, `{"tag_name":"v`+running+`"}`)
	h.probe = func() (string, string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.onDisk, method
	}
	h.upgrade = func(context.Context) ([]byte, error) {
		h.mu.Lock()
		h.upgrades++
		h.mu.Unlock()
		return upgrade()
	}
	return h
}

func (h *applyHarness) setOnDisk(version string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onDisk = version
}

func (h *applyHarness) upgradeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.upgrades
}

func (h *applyHarness) restart() bool {
	h.restarted <- struct{}{}
	return true
}

// noRestart stands in for a hub with no restart hook wired — one embedded in
// something other than `trau serve`.
func noRestart() bool { return false }

// waitApplyState blocks until the background apply settles on want.
func (h *applyHarness) waitApplyState(t *testing.T, want string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.Status().ApplyState.State; got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("applyState = %q, want %q", h.Status().ApplyState.State, want)
}

func okUpgrade() ([]byte, error) { return []byte("==> Upgrading trau\n"), nil }

func TestApplyRejectsNonBrewInstall(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.1.0", installOther, okUpgrade)

	if err := h.Apply(h.restart); !errors.Is(err, ErrNotBrew) {
		t.Fatalf("Apply on a non-brew install = %v, want ErrNotBrew", err)
	}
	if h.upgradeCount() != 0 {
		t.Error("brew ran for an install Homebrew does not own")
	}
	if got := h.Status().ApplyState.State; got != applyIdle {
		t.Errorf("applyState = %q, want %q after a refused apply", got, applyIdle)
	}
}

func TestApplyRestartsOnceDriftAppears(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, okUpgrade)
	h.upgrade = func(context.Context) ([]byte, error) {
		h.setOnDisk("2.2.0")
		return okUpgrade()
	}

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	select {
	case <-h.restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no restart after the upgrade left a newer binary on disk")
	}
	if got := h.Status().ApplyState.State; got != applyRunning {
		t.Errorf("applyState = %q, want %q while the restart is under way", got, applyRunning)
	}
}

// TestApplyRestartsWhenDriftWasAlreadyPending covers the already-up-to-date
// brew: nothing to upgrade, but the user asked to get current and the binary on
// disk is already ahead.
func TestApplyRestartsWhenDriftWasAlreadyPending(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.2.0", installBrew, func() ([]byte, error) {
		return []byte("Warning: trau 2.2.0 already installed\n"), nil
	})

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	select {
	case <-h.restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no restart with drift already pending")
	}
}

func TestApplyWithoutDriftReturnsToIdle(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, okUpgrade)

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h.waitApplyState(t, applyIdle)

	select {
	case <-h.restarted:
		t.Fatal("restarted with nothing new on disk")
	default:
	}
	if h.Status().CheckedAt == nil {
		t.Error("checkedAt is nil, want the remote check refreshed after a no-op upgrade")
	}
}

// TestApplyWithoutRestartHookSettlesToIdle covers the hub that cannot restart
// itself: the upgrade still lands, and the pending drift is what tells the story.
func TestApplyWithoutRestartHookSettlesToIdle(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, okUpgrade)
	h.upgrade = func(context.Context) ([]byte, error) {
		h.setOnDisk("2.2.0")
		return okUpgrade()
	}

	if err := h.Apply(noRestart); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h.waitApplyState(t, applyIdle)

	if !h.Status().RestartPending {
		t.Error("restartPending is false, want the un-restarted drift left visible")
	}
	if err := h.Apply(noRestart); err != nil {
		t.Fatalf("Apply after a hookless apply settled: %v", err)
	}
}

// TestApplyLogsFullBrewOutput pins the log to the full output on both paths: the
// API message carries only a tail, so the hub's log is the only complete record.
func TestApplyLogsFullBrewOutput(t *testing.T) {
	out := strings.Repeat("brew line\n", 30) + "final line"

	tests := []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"failure", errors.New("exit status 1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var log bytes.Buffer
			logger.Init(&log, false, false)
			t.Cleanup(func() { logger.Init(os.Stderr, false, false) })

			h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, func() ([]byte, error) {
				return []byte(out), tt.err
			})
			if err := h.Apply(h.restart); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if tt.err != nil {
				h.waitApplyState(t, applyFailed)
			} else {
				h.waitApplyState(t, applyIdle)
			}

			if !strings.Contains(log.String(), out) {
				t.Errorf("log = %q, want the full brew output", log.String())
			}
		})
	}
}

func TestApplyFailureKeepsOutputTail(t *testing.T) {
	lines := make([]string, 0, 30)
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("brew line %d", i))
	}
	out := strings.Join(lines, "\n")

	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, func() ([]byte, error) {
		return []byte(out), errors.New("exit status 1")
	})

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h.waitApplyState(t, applyFailed)

	got := h.Status().ApplyState.Message
	if want := strings.Join(lines[10:], "\n"); got != want {
		t.Errorf("message = %q, want the last %d lines", got, applyTailLines)
	}
	select {
	case <-h.restarted:
		t.Fatal("restarted after a failed upgrade")
	default:
	}
}

// TestApplyFailureWithoutOutputReportsError covers a brew that never ran at all:
// with no output to tail, the error itself is what the client gets.
func TestApplyFailureWithoutOutputReportsError(t *testing.T) {
	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, func() ([]byte, error) {
		return nil, errors.New("brew not found on PATH")
	})

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	h.waitApplyState(t, applyFailed)

	if got := h.Status().ApplyState.Message; got != "brew not found on PATH" {
		t.Errorf("message = %q, want the error", got)
	}
}

func TestApplyIsSingleFlight(t *testing.T) {
	release := make(chan struct{})
	h := newApplyHarness(t, "2.1.0", "2.1.0", installBrew, func() ([]byte, error) {
		<-release
		return okUpgrade()
	})

	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	h.waitApplyState(t, applyRunning)

	if err := h.Apply(h.restart); !errors.Is(err, ErrApplyInFlight) {
		t.Fatalf("second Apply = %v, want ErrApplyInFlight", err)
	}

	close(release)
	h.waitApplyState(t, applyIdle)

	if got := h.upgradeCount(); got != 1 {
		t.Errorf("brew ran %d times, want 1", got)
	}
	if err := h.Apply(h.restart); err != nil {
		t.Fatalf("Apply after the first settled: %v", err)
	}
}

func TestTailKeepsLastLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than n", "a\nb", 20, "a\nb"},
		{"trailing newline dropped", "a\nb\n", 20, "a\nb"},
		{"truncated to last n", "a\nb\nc\nd", 2, "c\nd"},
		{"empty", "", 20, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tail(tt.in, tt.n); got != tt.want {
				t.Errorf("tail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}
