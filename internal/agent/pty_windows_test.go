//go:build windows

package agent

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// startPTY must launch a bare bin found only on %PATH% while Dir points
// somewhere else entirely — the exact shape of the COD-1324 failure, where
// go-pty's raw CreateProcess probed the target repo for "claude" and never
// searched %PATH%. ConPTY has no separable slave end, so the transcript is
// polled rather than read to EOF.
func TestStartPTYResolvesABareBinViaPATH(t *testing.T) {
	sess, err := startPTY(t.Context(), "cmd", t.TempDir(), []string{"/c", "echo trau-pty-ok"}, 80, 24)
	if err != nil {
		t.Fatalf("startPTY: %v", err)
	}
	defer func() {
		_ = sess.Kill()
		_ = sess.Close()
	}()

	var mu sync.Mutex
	var buf bytes.Buffer
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, readErr := sess.Read(tmp)
			if n > 0 {
				mu.Lock()
				buf.Write(tmp[:n])
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		out := buf.String()
		mu.Unlock()
		if strings.Contains(out, "trau-pty-ok") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("child output never arrived; transcript so far: %q", buf.String())
}
