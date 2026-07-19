package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b    string
		wantCmp int
		wantOK  bool
	}{
		{"2.2.0", "2.1.0", 1, true},
		{"2.1.0", "2.2.0", -1, true},
		{"2.1.0", "2.1.0", 0, true},
		{"v2.2.0", "2.1.0", 1, true},
		{"2.2.0", "v2.2.0", 0, true},
		{"2.10.0", "2.9.0", 1, true},
		{"3.0.0", "2.99.99", 1, true},
		{"2.2", "2.1.0", 1, true},
		{"2.2.0", "dev", 0, false},
		{"dev", "2.2.0", 0, false},
		{"2.2.0-rc1", "2.2.0", 0, false},
		{"2.2.0.1", "2.2.0", 0, false},
		{"", "2.2.0", 0, false},
	}
	for _, tt := range tests {
		gotCmp, gotOK := compareVersions(tt.a, tt.b)
		if gotCmp != tt.wantCmp || gotOK != tt.wantOK {
			t.Errorf("compareVersions(%q, %q) = (%d, %v), want (%d, %v)",
				tt.a, tt.b, gotCmp, gotOK, tt.wantCmp, tt.wantOK)
		}
	}
}

// newTestChecker builds a Checker whose on-disk probe is fixed, so status can be
// asserted without a real binary on the machine.
func newTestChecker(running, onDisk, method string) *Checker {
	c := NewChecker(running)
	c.probe = func() (string, string) { return onDisk, method }
	return c
}

func TestStatusReportsDriftWithoutRemoteCheck(t *testing.T) {
	st := newTestChecker("2.1.0", "2.2.0", installBrew).Status()

	if !st.RestartPending {
		t.Error("restartPending = false, want true with a newer binary on disk")
	}
	if st.UpdateAvailable {
		t.Error("updateAvailable = true before any remote check")
	}
	if st.OnDisk != "2.2.0" || st.Running != "2.1.0" {
		t.Errorf("running/onDisk = %q/%q, want 2.1.0/2.2.0", st.Running, st.OnDisk)
	}
	if st.InstallMethod != installBrew {
		t.Errorf("installMethod = %q, want %q", st.InstallMethod, installBrew)
	}
	if st.CheckedAt != nil {
		t.Errorf("checkedAt = %v, want null before any check", st.CheckedAt)
	}
	if st.ReleaseURL != "" {
		t.Errorf("releaseUrl = %q, want empty with no known release", st.ReleaseURL)
	}
}

// TestStatusComparesLatestAgainstOnDisk pins the double-count rule: a release
// already sitting on disk is a pending restart, not an available update.
func TestStatusComparesLatestAgainstOnDisk(t *testing.T) {
	c := newTestChecker("2.1.0", "2.2.0", installBrew)
	c.endpoint = releaseServer(t, `{"tag_name":"v2.2.0"}`)

	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}

	st := c.Status()
	if st.Latest != "2.2.0" {
		t.Errorf("latest = %q, want 2.2.0 with the v stripped", st.Latest)
	}
	if !st.RestartPending {
		t.Error("restartPending = false, want true")
	}
	if st.UpdateAvailable {
		t.Error("updateAvailable = true, want false: the latest release is already on disk")
	}
	if st.CheckedAt == nil {
		t.Error("checkedAt = null after a successful check")
	}
	if want := "https://github.com/RomkaLTU/trau/releases/tag/v2.2.0"; st.ReleaseURL != want {
		t.Errorf("releaseUrl = %q, want %q", st.ReleaseURL, want)
	}
}

func TestStatusReportsUpdateAheadOfOnDisk(t *testing.T) {
	c := newTestChecker("2.2.0", "2.2.0", installBrew)
	c.endpoint = releaseServer(t, `{"tag_name":"v2.3.0"}`)

	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}

	st := c.Status()
	if !st.UpdateAvailable {
		t.Error("updateAvailable = false, want true with a newer release than the binary on disk")
	}
	if st.RestartPending {
		t.Error("restartPending = true, want false with no drift")
	}
}

// TestDevBuildNeverReportsUpdate keeps release nags off unversioned builds.
func TestDevBuildNeverReportsUpdate(t *testing.T) {
	c := newTestChecker("dev", "dev", installOther)
	c.endpoint = releaseServer(t, `{"tag_name":"v2.3.0"}`)

	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}

	st := c.Status()
	if st.UpdateAvailable {
		t.Error("updateAvailable = true on a dev build")
	}
	if st.RestartPending {
		t.Error("restartPending = true with dev on disk and dev running")
	}
}

// TestDisabledCheckerMakesNoRequest is the UPDATE_CHECK=0 guarantee: neither the
// scheduled loop nor a forced check reaches the network.
func TestDisabledCheckerMakesNoRequest(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"tag_name":"v2.3.0"}`))
	}))
	t.Cleanup(ts.Close)

	c := newTestChecker("2.2.0", "2.2.0", installBrew)
	c.endpoint = ts.URL
	c.SetEnabled(false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Run(ctx)
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow while disabled: %v", err)
	}

	if requests != 0 {
		t.Errorf("%d outbound requests, want 0 with checks disabled", requests)
	}
	st := c.Status()
	if st.ChecksEnabled {
		t.Error("checksEnabled = true, want false")
	}
	if st.Latest != "" {
		t.Errorf("latest = %q, want empty with checks disabled", st.Latest)
	}
}

// TestFailedCheckKeepsLastResult covers a GitHub blip: the UI keeps the answer
// it had rather than blanking out.
func TestFailedCheckKeepsLastResult(t *testing.T) {
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"tag_name":"v2.3.0"}`))
	}))
	t.Cleanup(ts.Close)

	c := newTestChecker("2.2.0", "2.2.0", installBrew)
	c.endpoint = ts.URL
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	before := c.Status()

	status = http.StatusServiceUnavailable
	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("CheckNow succeeded against a failing endpoint")
	}

	after := c.Status()
	if after.Latest != before.Latest || !after.CheckedAt.Equal(*before.CheckedAt) {
		t.Errorf("status changed after a failed check: %+v -> %+v", before, after)
	}
}

func releaseServer(t *testing.T, body string) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}
