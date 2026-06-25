package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/RomkaLTU/trau/internal/usage"
)

// clientVersion is the arbitrary clientInfo.version sent in the app-server
// handshake; the server only requires the field to be present.
const clientVersion = "1.0"

// codexProber reads codex's 5h/weekly windows. Primary path: a cold one-shot
// `codex app-server` JSON-RPC session (metadata-only, no prompt, sub-second).
// Fallback: the newest rollout JSONL's last token_count record, which is stale and
// null under exec mode — hence only a fallback. The two carry DIFFERENT schemas
// (camelCase vs snake_case) and use separate parsers on purpose.
type codexProber struct {
	bin string
}

func (p *codexProber) Probe(ctx context.Context) usage.Window {
	if w, ok := p.probeAppServer(ctx); ok {
		return w
	}
	if w, ok := probeCodexRollout(time.Now()); ok {
		return w
	}
	return usage.Window{}
}

// probeAppServer spawns `codex app-server`, performs the JSON-RPC handshake, asks
// for the rate limits, and returns the response matched by request id. The server
// is killed as soon as we have an answer or the deadline passes.
func (p *codexProber) probeAppServer(parent context.Context) (usage.Window, bool) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()

	bin := p.bin
	if bin == "" {
		bin = "codex"
	}
	cmd := exec.CommandContext(ctx, bin, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return usage.Window{}, false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return usage.Window{}, false
	}
	if err := cmd.Start(); err != nil {
		return usage.Window{}, false
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	const rateLimitsID = 2
	go func() {
		_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"trau","version":"`+clientVersion+`"},"capabilities":{"experimentalApi":true}}}`+"\n")
		_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","method":"initialized"}`+"\n")
		// The server needs a brief moment after the handshake before it answers
		// account/* methods; send the read once it has settled.
		select {
		case <-ctx.Done():
			return
		case <-time.After(600 * time.Millisecond):
		}
		_, _ = io.WriteString(stdin, `{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read"}`+"\n")
		// Leave stdin open; closing it early can abort the in-flight request.
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.ID == nil || *msg.ID != rateLimitsID || len(msg.Result) == 0 {
			continue
		}
		return parseCodexAppServer(msg.Result)
	}
	return usage.Window{}, false
}

// codexAppServerWindow mirrors RateLimitWindow from the app-server schema
// (camelCase; resetsAt is epoch seconds).
type codexAppServerWindow struct {
	UsedPercent float64 `json:"usedPercent"`
	ResetsAt    *int64  `json:"resetsAt"`
}

func parseCodexAppServer(raw json.RawMessage) (usage.Window, bool) {
	var resp struct {
		RateLimits struct {
			Primary   *codexAppServerWindow `json:"primary"`
			Secondary *codexAppServerWindow `json:"secondary"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return usage.Window{}, false
	}
	pick, label := resp.RateLimits.Primary, "5h"
	if s := resp.RateLimits.Secondary; s != nil && (pick == nil || s.UsedPercent > pick.UsedPercent) {
		pick, label = s, "weekly"
	}
	if pick == nil {
		return usage.Window{}, false
	}
	w := usage.Window{
		Available:      true,
		Provider:       "codex",
		Source:         "app-server",
		Label:          label,
		Utilization:    pick.UsedPercent,
		HasUtilization: true,
	}
	if pick.ResetsAt != nil && *pick.ResetsAt > 0 {
		w.ResetAt, w.HasReset = time.Unix(*pick.ResetsAt, 0), true
	}
	return w, true
}

// codexRolloutWindow mirrors the rollout-JSONL token_count schema (snake_case;
// resets are a relative second offset). Distinct from the app-server shape.
type codexRolloutWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	ResetsInSeconds *int64  `json:"resets_in_seconds"`
}

// probeCodexRollout reads the newest rollout file and returns the last token_count
// record's rate limits. Returns false when there is no file, no record, or the
// limits are null (exec mode never writes them).
func probeCodexRollout(now time.Time) (usage.Window, bool) {
	path := newestCodexRollout()
	if path == "" {
		return usage.Window{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return usage.Window{}, false
	}
	defer func() { _ = f.Close() }()

	var last json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var line map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if rl, ok := findRateLimits(line); ok {
			last = rl
		}
	}
	if last == nil {
		return usage.Window{}, false
	}

	var rl struct {
		Primary   *codexRolloutWindow `json:"primary"`
		Secondary *codexRolloutWindow `json:"secondary"`
	}
	if err := json.Unmarshal(last, &rl); err != nil {
		return usage.Window{}, false
	}
	pick, label := rl.Primary, "5h"
	if s := rl.Secondary; s != nil && (pick == nil || s.UsedPercent > pick.UsedPercent) {
		pick, label = s, "weekly"
	}
	if pick == nil {
		return usage.Window{}, false
	}
	w := usage.Window{
		Available:      true,
		Provider:       "codex",
		Source:         "rollout",
		Label:          label,
		Utilization:    pick.UsedPercent,
		HasUtilization: true,
	}
	if pick.ResetsInSeconds != nil && *pick.ResetsInSeconds > 0 {
		w.ResetAt, w.HasReset = now.Add(time.Duration(*pick.ResetsInSeconds)*time.Second), true
	}
	return w, true
}

// findRateLimits walks a decoded JSON object for the first non-null "rate_limits"
// value, tolerating the rollout record's varying nesting without hard-coding a path.
func findRateLimits(obj map[string]json.RawMessage) (json.RawMessage, bool) {
	if rl, ok := obj["rate_limits"]; ok && len(rl) > 0 && string(rl) != "null" {
		return rl, true
	}
	for _, v := range obj {
		var child map[string]json.RawMessage
		if err := json.Unmarshal(v, &child); err != nil {
			continue
		}
		if rl, ok := findRateLimits(child); ok {
			return rl, true
		}
	}
	return nil, false
}

// newestCodexRollout returns the most recently modified rollout JSONL under
// ~/.codex/sessions, or "" when none exist.
func newestCodexRollout() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = m, info.ModTime()
		}
	}
	return newest
}
