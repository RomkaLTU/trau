package agent

import (
	"sync"
	"time"
)

const (
	authQuietWindow        = 60 * time.Second
	authStreamingThreshold = 16 << 10
)

type authTimer interface {
	Stop() bool
}

type authTimerFactory func(time.Duration, func()) authTimer

type authDebouncer struct {
	mu              sync.Mutex
	signal          chan<- struct{}
	quietWindow     time.Duration
	newTimer        authTimerFactory
	timer           authTimer
	generation      uint64
	armed           bool
	bytesSinceMatch int
}

func newAuthDebouncer(
	signal chan<- struct{},
	quietWindow time.Duration,
	newTimer authTimerFactory,
) *authDebouncer {
	return &authDebouncer{
		signal:      signal,
		quietWindow: quietWindow,
		newTimer:    newTimer,
	}
}

func newAuthTimer(delay time.Duration, callback func()) authTimer {
	return time.AfterFunc(delay, callback)
}

func (d *authDebouncer) arm() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.generation++
	generation := d.generation
	d.armed = true
	d.bytesSinceMatch = 0
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = d.newTimer(d.quietWindow, func() {
		d.confirm(generation)
	})
}

func (d *authDebouncer) observeOutput(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.armed {
		return
	}
	d.bytesSinceMatch += n
	if d.bytesSinceMatch <= authStreamingThreshold {
		return
	}
	d.clearLocked()
}

func (d *authDebouncer) finish() {
	d.mu.Lock()
	confirm := d.armed
	d.clearLocked()
	d.mu.Unlock()

	if confirm {
		signalOnce(d.signal)
	}
}

func (d *authDebouncer) confirm(generation uint64) {
	d.mu.Lock()
	if !d.armed || generation != d.generation {
		d.mu.Unlock()
		return
	}
	d.clearLocked()
	d.mu.Unlock()

	signalOnce(d.signal)
}

func (d *authDebouncer) clearLocked() {
	d.generation++
	d.armed = false
	d.bytesSinceMatch = 0
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
