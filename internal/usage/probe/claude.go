package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/usage"
)

// claudeUsageURL is the undocumented endpoint Claude Code itself polls for the
// subscription 5h/weekly windows. The matching claude.ai host 403s; api.anthropic
// is the live one. Treated as fragile: any non-200 or decode failure fails closed.
const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// claudeOAuthBeta and the claude-code User-Agent are both required by the endpoint;
// omitting either yields a 401/403.
const claudeOAuthBeta = "oauth-2025-04-20"

// defaultClaudeVersion seeds the mandatory claude-code/<ver> User-Agent when the
// installed CLI version can't be read.
const defaultClaudeVersion = "2.1.191"

// claudeProber polls /api/oauth/usage with the user's existing OAuth token. It is
// metadata-only (no model call) and behind the master USAGE_WINDOW flag because
// the endpoint is undocumented.
type claudeProber struct {
	bin     string
	version string // cached User-Agent version, resolved lazily
	client  *http.Client
}

func (p *claudeProber) Probe(ctx context.Context) usage.Window {
	tok := claudeOAuthToken(ctx)
	if tok == "" {
		return usage.Window{}
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return usage.Window{}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-beta", claudeOAuthBeta)
	req.Header.Set("User-Agent", "claude-code/"+p.uaVersion(ctx))

	resp, err := p.client.Do(req)
	if err != nil {
		return usage.Window{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return usage.Window{}
	}
	var body struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return usage.Window{}
	}
	return bindingClaudeWindow(body.FiveHour, body.SevenDay)
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// bindingClaudeWindow picks the more-constrained of the two windows — the one a
// run will actually hit first — as the bar, labelling which it is. The 5h and 7d
// windows reset independently, so the binding one is simply the higher utilization.
func bindingClaudeWindow(fiveHour, sevenDay *claudeWindow) usage.Window {
	pick, label := fiveHour, "5h"
	if sevenDay != nil && (fiveHour == nil || sevenDay.Utilization > fiveHour.Utilization) {
		pick, label = sevenDay, "7d"
	}
	if pick == nil {
		return usage.Window{}
	}
	w := usage.Window{
		Available:      true,
		Provider:       "claude",
		Source:         "oauth",
		Label:          label,
		Utilization:    pick.Utilization,
		HasUtilization: true,
	}
	if t, ok := parseTime(pick.ResetsAt); ok {
		w.ResetAt, w.HasReset = t, true
	}
	return w
}

// uaVersion resolves the installed CLI version for the User-Agent, caching it.
// Failure falls back to defaultClaudeVersion rather than blocking the probe.
func (p *claudeProber) uaVersion(ctx context.Context) string {
	if p.version != "" {
		return p.version
	}
	p.version = defaultClaudeVersion
	if p.bin == "" {
		return p.version
	}
	out, err := exec.CommandContext(ctx, p.bin, "--version").Output()
	if err == nil {
		if v := strings.Fields(strings.TrimSpace(string(out))); len(v) > 0 && v[0] != "" {
			p.version = v[0]
		}
	}
	return p.version
}

// claudeOAuthToken returns the user's Claude Code OAuth access token from the
// credentials file, falling back to the macOS Keychain item Claude Code writes.
// Empty when neither yields a token (the probe then fails closed).
func claudeOAuthToken(ctx context.Context) string {
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json")); err == nil {
			if tok := parseClaudeToken(b); tok != "" {
				return tok
			}
		}
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
		if err == nil {
			if tok := parseClaudeToken(out); tok != "" {
				return tok
			}
		}
	}
	return ""
}

func parseClaudeToken(b []byte) string {
	var creds struct {
		ClaudeAIOAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &creds); err != nil {
		return ""
	}
	return strings.TrimSpace(creds.ClaudeAIOAuth.AccessToken)
}

// parseTime accepts the provider reset encodings that arrive as text — RFC-3339
// with or without fractional seconds.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
