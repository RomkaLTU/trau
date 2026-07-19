// Package logger provides small, on-demand diagnostic output for the trau CLI.
//
// It is intentionally not a structured logging framework: two boolean levels
// (--verbose and --debug), output to stderr only, and no effect on the
// byte-stable stdout / --json contract.
package logger

import (
	"fmt"
	"io"
	"os"
	"sync"
)

var (
	out            io.Writer = os.Stderr
	mu             sync.Mutex
	verboseEnabled bool
	debugEnabled   bool
)

// Init sets the output sink and enable flags. Call once in main.
func Init(w io.Writer, verbose, debug bool) {
	mu.Lock()
	defer mu.Unlock()
	if w != nil {
		out = w
	}
	verboseEnabled = verbose
	debugEnabled = debug
}

// SetVerbose toggles verbose output at runtime.
func SetVerbose(v bool) {
	mu.Lock()
	defer mu.Unlock()
	verboseEnabled = v
}

// SetDebug toggles debug output at runtime.
func SetDebug(d bool) {
	mu.Lock()
	defer mu.Unlock()
	debugEnabled = d
}

// Printf writes a diagnostic line to stderr whatever the flags are, for the few
// records worth keeping unconditionally — a long-running hub's stderr is its log.
func Printf(format string, a ...any) {
	emit("info", format, a...)
}

// Verbosef writes a verbose diagnostic line to stderr when verbose or debug
// mode is enabled (--debug implies --verbose).
func Verbosef(format string, a ...any) {
	mu.Lock()
	on := verboseEnabled || debugEnabled
	mu.Unlock()
	if !on {
		return
	}
	emit("verbose", format, a...)
}

// Debugf writes a debug diagnostic line to stderr only when debug mode is
// enabled.
func Debugf(format string, a ...any) {
	mu.Lock()
	on := debugEnabled
	mu.Unlock()
	if !on {
		return
	}
	emit("debug", format, a...)
}

func emit(level, format string, a ...any) {
	mu.Lock()
	w := out
	mu.Unlock()
	_, _ = fmt.Fprintf(w, "[trau:%s] %s\n", level, fmt.Sprintf(format, a...))
}
