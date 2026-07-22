package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/RomkaLTU/trau/internal/event"
	"github.com/RomkaLTU/trau/internal/hubclient"
)

// qaCaptureMax caps how many discovered accounts one verify can hand back
// across all of its attempts, so a verifier that dumps every credential it finds
// in the repo cannot fill the roster with logins nothing signs in with.
const qaCaptureMax = 5

// qaDiscoveredLabel names a captured account the verifier handed back unlabeled.
const qaDiscoveredLabel = "Discovered account"

// qaCapturePath is the verifier's side channel for credentials it discovered
// inside the repo under test and signed in with.
func qaCapturePath(id string) string { return "/tmp/qa-capture-" + id + ".json" }

type qaCaptureFile struct {
	Accounts []qaCaptureAccount `json:"accounts"`
}

type qaCaptureAccount struct {
	Label       string `json:"label"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
}

// ingestQACapture reads the capture file a verify attempt may have left behind
// and stores each newly discovered credential on the hub as an agent-captured QA
// account, so the next run's roster is prefilled. Accounts already in the roster
// the verifier was handed are skipped, and the file is deleted either way — a
// discovered secret lives on disk only between the attempt and this call. Every
// failure here is a warning: capture is an optimization for the next run, never
// a condition of this one.
//
// Every attempt of a verify runs this — the repair retries and the browser
// re-verify included — so what one attempt stored is folded back into the roster
// and counted against the cap, and a later attempt offering the same credential
// under a different label sees it as known.
func (p *Pipeline) ingestQACapture(ctx context.Context, id string) {
	path := qaCapturePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	if p.SaveQAAccount == nil {
		return
	}

	var file qaCaptureFile
	if err := json.Unmarshal(data, &file); err != nil {
		p.logf("  ⚠ QA capture ignored — unreadable capture file: %v", err)
		return
	}

	candidates := validQACaptures(file.Accounts)
	budget := qaCaptureMax - p.qaCaptured
	if len(candidates) > budget {
		p.logf("  ⚠ QA capture: %d accounts offered, keeping the first %d", len(candidates), budget)
		candidates = candidates[:budget]
	}

	known := make(map[string]bool, len(p.qaRoster))
	labels := make(map[string]bool, len(p.qaRoster))
	for _, a := range p.qaRoster {
		known[qaUsernameKey(a.Username)] = true
		labels[a.Label] = true
	}

	for _, c := range candidates {
		if known[qaUsernameKey(c.Username)] {
			continue
		}
		label := qaCaptureLabel(c)
		if labels[label] {
			label += " (captured)"
		}
		in := hubclient.QAAccountInput{
			Label:       label,
			Username:    c.Username,
			Secret:      c.Secret,
			Description: c.Description,
			Source:      hubclient.QASourceAgent,
		}
		if err := p.SaveQAAccount(ctx, in); err != nil {
			p.logf("  ⚠ QA account not captured: %v", err)
			continue
		}
		known[qaUsernameKey(c.Username)] = true
		labels[label] = true
		p.qaRoster = append(p.qaRoster, hubclient.QAAccount{
			Label:       label,
			Username:    c.Username,
			Secret:      c.Secret,
			Description: c.Description,
		})
		p.qaCaptured++
		p.logf("  ✓ QA account captured: %s", label)
		if p.Events != nil {
			p.Events.Emit(event.KindQACaptured, "verify", "QA account captured: "+label, map[string]any{"ticket": id, "label": label})
		}
	}
}

// validQACaptures trims the offered entries and drops the ones that cannot be
// signed in with.
func validQACaptures(accounts []qaCaptureAccount) []qaCaptureAccount {
	out := make([]qaCaptureAccount, 0, len(accounts))
	for _, a := range accounts {
		a.Username = strings.TrimSpace(a.Username)
		a.Secret = strings.TrimSpace(a.Secret)
		if a.Username == "" || a.Secret == "" {
			continue
		}
		a.Label = strings.TrimSpace(a.Label)
		a.Description = strings.TrimSpace(a.Description)
		out = append(out, a)
	}
	return out
}

// qaCaptureLabel is the name a captured account is known by everywhere a
// credential value may not go — the roster, the loop log, the qa_captured event.
// An entry that named no label, or named its own username or secret as one, gets
// the generic label rather than a credential standing in for it.
func qaCaptureLabel(a qaCaptureAccount) string {
	namesCredential := qaUsernameKey(a.Label) == qaUsernameKey(a.Username) || a.Label == a.Secret
	if a.Label == "" || namesCredential {
		return qaDiscoveredLabel
	}
	return a.Label
}

func qaUsernameKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}
