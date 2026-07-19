// Package update tracks how the running hub compares to the trau binary on disk
// and to the newest release on GitHub. The two facts are distinct: a `brew
// upgrade trau` leaves a newer binary on disk while the hub keeps serving the
// version it booted with, and only a restart closes that gap.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RomkaLTU/trau/internal/logger"
)

// ReleasesURL is where a user updates an install trau cannot update itself.
const ReleasesURL = "https://github.com/RomkaLTU/trau/releases"

const (
	latestReleaseAPI = "https://api.github.com/repos/RomkaLTU/trau/releases/latest"
	releaseTagURL    = ReleasesURL + "/tag/v"

	probeTTL      = time.Minute
	fetchTimeout  = 5 * time.Second
	startupDelay  = 5 * time.Second
	checkInterval = 24 * time.Hour
)

// Status is the /api/v1/update resource.
type Status struct {
	Running         string     `json:"running"`
	OnDisk          string     `json:"onDisk"`
	Latest          string     `json:"latest"`
	RestartPending  bool       `json:"restartPending"`
	UpdateAvailable bool       `json:"updateAvailable"`
	InstallMethod   string     `json:"installMethod"`
	CheckedAt       *time.Time `json:"checkedAt"`
	ChecksEnabled   bool       `json:"checksEnabled"`
	ReleaseURL      string     `json:"releaseUrl"`
	ApplyState      ApplyState `json:"applyState"`
}

// Checker holds what the hub knows about newer trau versions. The on-disk probe
// is lazy and cached, so nothing runs until a client asks; the remote check runs
// on its own schedule and keeps its last successful answer across failures.
type Checker struct {
	running  string
	client   *http.Client
	endpoint string
	probe    func() (version, method string)
	upgrade  func(context.Context) ([]byte, error)

	mu           sync.Mutex
	enabled      bool
	onDisk       string
	method       string
	probedAt     time.Time
	latest       string
	checkedAt    time.Time
	applyState   string
	applyMessage string
}

// NewChecker builds a Checker for a hub running version, with remote checks on.
func NewChecker(running string) *Checker {
	return &Checker{
		running:    running,
		client:     &http.Client{Timeout: fetchTimeout},
		endpoint:   latestReleaseAPI,
		probe:      probeBinary,
		upgrade:    brewUpgrade,
		enabled:    true,
		applyState: applyIdle,
	}
}

// SetEnabled gates the remote check (UPDATE_CHECK). On-disk drift detection is
// unaffected: it never leaves the machine.
func (c *Checker) SetEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = enabled
}

// Status reports both facts. It compares the latest release against the version
// on disk rather than the running one, so a pending restart and an available
// update never count the same delta twice.
func (c *Checker) Status() Status {
	onDisk, method := c.local()

	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{
		Running:        c.running,
		OnDisk:         onDisk,
		Latest:         c.latest,
		RestartPending: onDisk != "" && onDisk != c.running,
		InstallMethod:  method,
		ChecksEnabled:  c.enabled,
		ApplyState:     ApplyState{State: c.applyState, Message: c.applyMessage},
	}
	if !c.checkedAt.IsZero() {
		at := c.checkedAt
		st.CheckedAt = &at
	}
	if c.latest != "" {
		st.ReleaseURL = releaseTagURL + c.latest
		installed := onDisk
		if installed == "" {
			installed = c.running
		}
		cmp, ok := compareVersions(c.latest, installed)
		st.UpdateAvailable = ok && cmp > 0
	}
	return st
}

// Run checks GitHub shortly after boot and once a day after that. It returns
// straight away when checks are off, so UPDATE_CHECK=0 makes no request at all.
func (c *Checker) Run(ctx context.Context) {
	c.mu.Lock()
	enabled := c.enabled
	c.mu.Unlock()
	if !enabled {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupDelay):
	}

	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		if err := c.CheckNow(ctx); err != nil {
			logger.Verbosef("update check: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CheckNow fetches the newest release tag and records it with the time it was
// read. A failure leaves the previous answer in place; a disabled checker skips
// the request entirely.
func (c *Checker) CheckNow(ctx context.Context) error {
	c.mu.Lock()
	enabled := c.enabled
	c.mu.Unlock()
	if !enabled {
		return nil
	}

	tag, err := c.fetchLatest(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest, c.checkedAt = tag, time.Now()
	return nil
}

// local returns the cached on-disk probe, re-running it once the cache is stale.
// The probe execs a subprocess, so it runs outside the lock.
func (c *Checker) local() (version, method string) {
	c.mu.Lock()
	if !c.probedAt.IsZero() && time.Since(c.probedAt) < probeTTL {
		version, method = c.onDisk, c.method
		c.mu.Unlock()
		return version, method
	}
	c.mu.Unlock()

	version, method = c.probe()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.onDisk, c.method, c.probedAt = version, method, time.Now()
	return version, method
}

func (c *Checker) fetchLatest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch latest release: %s", res.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	tag := strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if tag == "" {
		return "", errors.New("latest release carries no tag")
	}
	return tag, nil
}

// compareVersions orders a and b as dotted numeric versions, ignoring a leading
// "v". ok is false when either side is not one — "dev" builds and pre-release
// tags compare to nothing, which is how they stay out of update claims.
func compareVersions(a, b string) (cmp int, ok bool) {
	pa, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	pb, ok := parseVersion(b)
	if !ok {
		return 0, false
	}
	for i := range pa {
		switch {
		case pa[i] > pb[i]:
			return 1, true
		case pa[i] < pb[i]:
			return -1, true
		}
	}
	return 0, true
}

func parseVersion(v string) (parts [3]int, ok bool) {
	fields := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(fields) > len(parts) {
		return parts, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return parts, false
		}
		parts[i] = n
	}
	return parts, true
}
