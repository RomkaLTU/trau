package webserver

import (
	"os/exec"
	"strings"
	"testing"
)

func TestTailWriterKeepsLastBytes(t *testing.T) {
	w := newTailWriter(8)
	for _, chunk := range []string{"hello ", "world", "!!!!!"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := w.String(); got != "rld!!!!!" {
		t.Errorf("tail = %q, want the last 8 bytes %q", got, "rld!!!!!")
	}
}

func TestTailWriterUnderCapKeepsAll(t *testing.T) {
	w := newTailWriter(64)
	if _, err := w.Write([]byte("short line")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.String(); got != "short line" {
		t.Errorf("tail = %q, want the whole write", got)
	}
}

func TestCaptureErrorSurfacesFirstStderrLine(t *testing.T) {
	_, err := exec.Command("sh", "-c", "echo 'trau: refusing to start a nested Trau loop' >&2; echo '  → run trau doctor' >&2; exit 1").Output()
	got := captureError(err)
	if got == nil || !strings.Contains(got.Error(), "refusing to start a nested Trau loop") {
		t.Errorf("captureError = %v, want the child's first stderr line surfaced", got)
	}
}

func TestCaptureErrorKeepsSilentExitUnchanged(t *testing.T) {
	_, err := exec.Command("sh", "-c", "exit 1").Output()
	if got := captureError(err); got != err {
		t.Errorf("captureError = %v, want the original error when stderr is empty", got)
	}
}
