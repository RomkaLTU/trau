package agent

import (
	"errors"
	"testing"
)

func TestRateLimitedTextMatchesProviderWalls(t *testing.T) {
	walls := []string{
		"rate_limit exceeded",
		"Rate limit reached for requests",
		"usage limit reached, try again later",
		"quota exceeded for this org",
		"You exceeded your current quota, please check your plan",
		"kimi run (verify): HTTP 429 Too Many Requests",
		"Error: 429 rate_limit exceeded",
		`{"type":"error","error":{"type":"rate_limit_error"}}`,
		"API Error: status 429",
		"\x1b[31mrate limit\x1b[0m reached",
	}
	for _, text := range walls {
		if !RateLimitedText(text) {
			t.Errorf("RateLimitedText(%q) = false, want true", text)
		}
	}
}

// The hub classifies a whole turn of raw provider JSON, so anything that shows up
// in an ordinary envelope must not read as a wall. Every string here was a false
// positive under the previous bare `strings.Contains(text, "429")` check, which
// stalled healthy grill sessions as "usage or rate limit".
func TestRateLimitedTextIgnoresIncidentalDigitsAndProse(t *testing.T) {
	healthy := []string{
		`{"cache_read_input_tokens":14290}`,
		`{"duration_ms":4290,"duration_api_ms":429}`,
		`{"total_cost_usd":0.04291}`,
		`{"session_id":"9efc39bf-df51-4077-86c9-eec459206610"}`,
		`{"uuid":"c1a429b7-0000-4000-8000-000000000000"}`,
		`{"output_tokens":429000}`,
		`{"is_error":true,"result":429}`,
		`{"error":null,"num_turns":429}`,
		"listening on 127.0.0.1:8728, pid 4291",
		"the team agreed on a disk quota for the build cache",
		`exec: "C:\\PROJECTS\\trau\\claude": executable file not found in %PATH%`,
		"",
	}
	for _, text := range healthy {
		if RateLimitedText(text) {
			t.Errorf("RateLimitedText(%q) = true, want false", text)
		}
	}
}

func TestIsRateLimitedUnwraps(t *testing.T) {
	if IsRateLimited(nil) {
		t.Error("IsRateLimited(nil) = true, want false")
	}
	wrapped := errors.New("claude interactive run (build): 429 usage limit reached")
	if !IsRateLimited(wrapped) {
		t.Error("IsRateLimited on a usage-limit error = false, want true")
	}
	if IsRateLimited(errors.New("build failed: file not found")) {
		t.Error("a plain build failure must not be flagged as rate-limited")
	}
}
