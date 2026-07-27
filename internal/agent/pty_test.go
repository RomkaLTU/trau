//go:build unix

package agent

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"
)

// readAll consumes sess until a read fails, which on a pty is how the drain
// learns the child is gone. The bool reports whether that happened within d.
func readAll(t *testing.T, sess terminalSession, d time.Duration) ([]byte, bool) {
	t.Helper()
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, sess)
		done <- buf.Bytes()
	}()
	select {
	case out := <-done:
		return out, true
	case <-time.After(d):
		return nil, false
	}
}

func TestStartPTYGivesTheChildTheRequestedGeometry(t *testing.T) {
	sess, err := startPTY(t.Context(), "sh", t.TempDir(), []string{"-c", "stty size"}, 137, 41)
	if err != nil {
		t.Fatalf("startPTY: %v", err)
	}
	defer func() { _ = sess.Close() }()

	out, ok := readAll(t, sess, 5*time.Second)
	if !ok {
		t.Fatal("terminal never ended its output stream after the child exited")
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("child exited with %v", err)
	}
	if got := string(bytes.TrimSpace(out)); got != "41 137" {
		t.Errorf("stty size = %q, want %q", got, "41 137")
	}
}

// Everything downstream of the seam consumes this stream raw, so escape
// sequences must arrive exactly as written.
func TestStartPTYTeesChildBytesUnaltered(t *testing.T) {
	const payload = `\033[1;31mred\033[0m\033]0;title\a`

	sess, err := startPTY(t.Context(), "sh", t.TempDir(), []string{"-c", "printf '" + payload + "'"}, 80, 24)
	if err != nil {
		t.Fatalf("startPTY: %v", err)
	}
	defer func() { _ = sess.Close() }()

	out, ok := readAll(t, sess, 5*time.Second)
	if !ok {
		t.Fatal("terminal never ended its output stream after the child exited")
	}
	if want := "\x1b[1;31mred\x1b[0m\x1b]0;title\a"; string(out) != want {
		t.Errorf("transcript bytes = %q, want %q", out, want)
	}
}

func TestStartPTYKillEndsTheChild(t *testing.T) {
	sess, err := startPTY(t.Context(), "sh", t.TempDir(), []string{"-c", "sleep 60"}, 80, 24)
	if err != nil {
		t.Fatalf("startPTY: %v", err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	select {
	case err := <-waited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Errorf("Wait after Kill = %v, want an exec.ExitError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Kill")
	}
}
