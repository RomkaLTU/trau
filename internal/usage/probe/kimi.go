package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/RomkaLTU/trau/internal/usage"
)

// kimiBalanceURL is Moonshot's platform balance endpoint. It needs a platform API
// key (KIMI_API_KEY) — the Kimi Code subscription token does not authenticate
// here, and the subscription exposes no balance/window of its own (use the PTY
// fallback for subscription usage).
const kimiBalanceURL = "https://api.moonshot.ai/v1/users/me/balance"

// kimiBalanceProber reports a Moonshot prepaid balance. Kimi has no rolling
// rate-limit window, so this is a balance figure, never a utilization bar — the
// HUD shows the dollars and no denominator.
type kimiBalanceProber struct {
	apiKey string
	client *http.Client
}

func (p *kimiBalanceProber) Probe(ctx context.Context) usage.Window {
	if p.apiKey == "" {
		return usage.Window{}
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kimiBalanceURL, nil)
	if err != nil {
		return usage.Window{}
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return usage.Window{}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return usage.Window{}
	}
	var body struct {
		Data struct {
			AvailableBalance *float64 `json:"available_balance"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Data.AvailableBalance == nil {
		return usage.Window{}
	}
	return usage.Window{
		Available:  true,
		Provider:   "kimi",
		Source:     "balance",
		Label:      "balance",
		BalanceUSD: *body.Data.AvailableBalance,
		HasBalance: true,
	}
}
