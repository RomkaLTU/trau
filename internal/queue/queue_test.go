package queue

import (
	"path/filepath"
	"testing"
)

func TestReportRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", ".drain-report")
	want := DrainReport{Class: "faulted", Reason: "provider timed out"}
	if err := WriteReport(path, want); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	got, ok := ReadReport(path)
	if !ok {
		t.Fatal("ReadReport ok = false, want true")
	}
	if got != want {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
}

func TestReadReportMissingFile(t *testing.T) {
	if _, ok := ReadReport(filepath.Join(t.TempDir(), "absent")); ok {
		t.Fatal("ReadReport of a missing file reported ok = true")
	}
}
