package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomkaLTU/trau/internal/checks"
)

// --- rate-limit classification (the 429-cascade guard) --------------------

func TestAgentErrSummaryDetectsRateLimit(t *testing.T) {
	rateLimited := []string{
		"rate_limit exceeded",
		"Rate limit reached for requests",
		"usage limit reached, try again later",
		"quota exceeded for this org",
		"kimi run (verify): HTTP 429 Too Many Requests",
	}
	for _, msg := range rateLimited {
		_, rl := agentErrSummary(errors.New(msg))
		if !rl {
			t.Errorf("agentErrSummary(%q) rateLimited = false, want true", msg)
		}
		if !isRateLimited(errors.New(msg)) {
			t.Errorf("isRateLimited(%q) = false, want true", msg)
		}
	}
}

func TestAgentErrSummaryNonRateLimit(t *testing.T) {
	// A line that itself starts with "error:" is surfaced verbatim.
	msg, rl := agentErrSummary(errors.New("build failed\nError: file not found\nstack trace here"))
	if rl {
		t.Error("a plain error must not be flagged as rate-limited")
	}
	if msg != "Error: file not found" {
		t.Errorf("summary = %q, want the error: line surfaced", msg)
	}
}

func TestAgentErrSummaryFirstLineFallback(t *testing.T) {
	msg, rl := agentErrSummary(errors.New("something broke\nmore detail\neven more"))
	if rl || msg != "something broke" {
		t.Errorf("got (%q,%v), want (\"something broke\", false)", msg, rl)
	}
}

func TestProviderOf(t *testing.T) {
	if got := providerOf(errors.New("kimi run (verify): boom")); got != "kimi" {
		t.Errorf("providerOf = %q, want kimi", got)
	}
	if got := providerOf(errors.New("no recognizable prefix")); got != "provider" {
		t.Errorf("providerOf fallback = %q, want provider", got)
	}
}

func TestIsFatalAgentErr(t *testing.T) {
	if !isFatalAgentErr(&PausedError{ID: "COD-1"}) {
		t.Error("a PausedError must be fatal to the panel")
	}
	if !isFatalAgentErr(&GiveUpError{ID: "COD-1"}) {
		t.Error("a GiveUpError must be fatal to the panel")
	}
	if !isFatalAgentErr(fmt.Errorf("wrapped: %w", &GiveUpError{ID: "COD-1"})) {
		t.Error("a wrapped GiveUpError must still be fatal")
	}
	if isFatalAgentErr(errors.New("plain timeout")) {
		t.Error("a plain error must NOT be fatal (counts as one verifier failing)")
	}
}

// --- panel merge & policy -------------------------------------------------

func TestNormalizePolicy(t *testing.T) {
	tests := map[string]string{
		"majority": "majority", "MAJORITY": "majority",
		"any-pass": "any-pass", "any_pass": "any-pass", "anypass": "any-pass",
		"": "unanimous", "unanimous": "unanimous", "weird": "unanimous",
	}
	for in, want := range tests {
		if got := normalizePolicy(in); got != want {
			t.Errorf("normalizePolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPanelPasses(t *testing.T) {
	tests := []struct {
		policy        string
		passes, total int
		want          bool
	}{
		{"unanimous", 2, 2, true}, {"unanimous", 1, 2, false},
		{"majority", 2, 3, true}, {"majority", 1, 2, false}, // a 1/2 tie is not a majority
		{"majority", 2, 2, true}, // full consensus is also a majority
		{"any-pass", 1, 3, true}, {"any-pass", 0, 2, false},
		{"unanimous", 0, 0, false}, // no verifiers never passes
	}
	for _, tc := range tests {
		if got := panelPasses(tc.policy, tc.passes, tc.total); got != tc.want {
			t.Errorf("panelPasses(%q,%d,%d) = %v, want %v", tc.policy, tc.passes, tc.total, got, tc.want)
		}
	}
}

func TestMergeVerdictsUnanimousDissentTagged(t *testing.T) {
	results := []panelResult{
		{Name: "claude", Verdict: verdict{Pass: true}},
		{Name: "codex", Verdict: verdict{Pass: false, Failures: []string{"login redirect broken"}}},
	}
	m := mergeVerdicts("unanimous", results)
	if m.Pass {
		t.Error("unanimous: any dissent must fail the merged verdict")
	}
	if len(m.Failures) != 1 || !strings.Contains(m.Failures[0], "[codex]") || !strings.Contains(m.Failures[0], "login redirect broken") {
		t.Errorf("dissent should be carried over tagged by member, got %v", m.Failures)
	}
	if !strings.Contains(m.Summary, "dissent: codex") {
		t.Errorf("summary should name the dissenter, got %q", m.Summary)
	}
}

func TestMergeVerdictsAllPass(t *testing.T) {
	results := []panelResult{
		{Name: "claude", Verdict: verdict{Pass: true}},
		{Name: "codex", Verdict: verdict{Pass: true}},
	}
	m := mergeVerdicts("unanimous", results)
	if !m.Pass {
		t.Error("all members passing must pass the merge")
	}
	if len(m.Failures) != 0 {
		t.Errorf("a passing merge carries no failures, got %v", m.Failures)
	}
}

func TestMergeVerdictsAnyPass(t *testing.T) {
	results := []panelResult{
		{Name: "claude", Verdict: verdict{Pass: false, Failures: []string{"x"}}},
		{Name: "codex", Verdict: verdict{Pass: true}},
	}
	if m := mergeVerdicts("any-pass", results); !m.Pass {
		t.Errorf("any-pass with one passer must pass, got %+v", m)
	}
}

func TestMergeVerdictsFailsClosedWithSummary(t *testing.T) {
	// A member fails but reports neither failures nor a summary — the merge must
	// still surface at least the panel summary so repair has something to act on.
	results := []panelResult{
		{Name: "claude", Verdict: verdict{Pass: false}},
	}
	m := mergeVerdicts("unanimous", results)
	if m.Pass || len(m.Failures) == 0 {
		t.Errorf("a failing merge must carry at least one failure line, got %+v", m)
	}
}

// --- check severity gating ------------------------------------------------

func TestGateChecksBlocksOnErrorSeverity(t *testing.T) {
	library := []checks.Check{
		{Name: "tests", Severity: "error"},
		{Name: "lint", Severity: "warn"},
	}
	v := verdict{
		Pass: true, // the agent claimed pass...
		Checks: []checkResult{
			{Name: "tests", Pass: false, Detail: "2 failing"},
			{Name: "lint", Pass: false, Detail: "style nit"},
		},
	}
	got, warnings := gateChecks(library, v)
	if got.Pass {
		t.Error("a failing error-severity check must force pass=false")
	}
	if !containsLine(got.Failures, "[check:tests] 2 failing") {
		t.Errorf("error check should fold into failures, got %v", got.Failures)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "[check:lint]") {
		t.Errorf("warn check should surface as a non-blocking warning, got %v", warnings)
	}
}

func TestGateChecksLibrarySeverityWinsOverEcho(t *testing.T) {
	// The verifier echoes severity "warn" for tests, but the library declares it
	// "error" — the library wins, so the check still blocks (no silent downgrade).
	library := []checks.Check{{Name: "tests", Severity: "error"}}
	v := verdict{Pass: true, Checks: []checkResult{{Name: "tests", Severity: "warn", Pass: false, Detail: "boom"}}}
	got, _ := gateChecks(library, v)
	if got.Pass {
		t.Error("library severity must override the echoed severity (error blocks)")
	}
}

func TestGateChecksUnknownCheckUsesEchoedSeverity(t *testing.T) {
	// A check not in the library falls back to the severity the verifier echoed.
	library := []checks.Check{{Name: "tests", Severity: "error"}}
	v := verdict{Pass: true, Checks: []checkResult{{Name: "mystery", Severity: "error", Pass: false, Detail: "x"}}}
	got, _ := gateChecks(library, v)
	if got.Pass {
		t.Error("an unknown error-severity check should still block via its echoed severity")
	}
}

func TestGateChecksNoOpWhenNothingToGate(t *testing.T) {
	v := verdict{Pass: true}
	got, warnings := gateChecks(nil, v)
	if !got.Pass || warnings != nil {
		t.Errorf("no library/checks should leave the verdict untouched, got %+v / %v", got, warnings)
	}
}

func TestGateChecksEmptyDetail(t *testing.T) {
	library := []checks.Check{{Name: "tests", Severity: "error"}}
	v := verdict{Pass: true, Checks: []checkResult{{Name: "tests", Pass: false}}}
	got, _ := gateChecks(library, v)
	if !containsLine(got.Failures, "[check:tests] failed") {
		t.Errorf("empty detail should render as 'failed', got %v", got.Failures)
	}
}

// --- verdict I/O & failure formatting -------------------------------------

func TestVerdictRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verify.json")
	want := verdict{Pass: false, Summary: "nope", Failures: []string{"a", "b"}}
	if err := writeVerdictFile(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := readVerdict(path)
	if !ok {
		t.Fatal("readVerdict ok = false, want true")
	}
	if got.Pass != want.Pass || got.Summary != want.Summary || len(got.Failures) != 2 {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestReadVerdictMissingAndMalformed(t *testing.T) {
	if _, ok := readVerdict(filepath.Join(t.TempDir(), "nope.json")); ok {
		t.Error("missing verdict should read ok=false")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("{not json"), 0o644)
	if _, ok := readVerdict(bad); ok {
		t.Error("malformed verdict should read ok=false")
	}
}

func TestWriteFailureVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.json")
	if err := writeFailureVerdict(path, "agent timed out"); err != nil {
		t.Fatal(err)
	}
	v, _ := readVerdict(path)
	if v.Pass || v.Summary != "agent timed out" || len(v.Failures) != 1 {
		t.Errorf("failure verdict = %+v, want pass=false with the reason as the sole failure", v)
	}
}

func TestFailureLines(t *testing.T) {
	// Caps at 20 lines.
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, fmt.Sprintf("f%d", i))
	}
	got := (verdict{Failures: many}).failureLines()
	if n := len(strings.Split(got, "\n")); n != 20 {
		t.Errorf("failureLines emitted %d lines, want capped at 20", n)
	}
	// Falls back to summary, then to a placeholder.
	if got := (verdict{Summary: "just a summary"}).failureLines(); got != "just a summary" {
		t.Errorf("failureLines summary fallback = %q", got)
	}
	if got := (verdict{}).failureLines(); got != "see verdict" {
		t.Errorf("failureLines empty fallback = %q, want 'see verdict'", got)
	}
}

func TestTopFailures(t *testing.T) {
	got := topFailures(verdict{Failures: []string{"a", "b", "c", "d"}})
	if len(got) != 3 {
		t.Errorf("topFailures = %v, want first 3", got)
	}
	if got := topFailures(verdict{Summary: "s"}); len(got) != 1 || got[0] != "s" {
		t.Errorf("topFailures summary fallback = %v", got)
	}
	if got := topFailures(verdict{}); got != nil {
		t.Errorf("topFailures empty = %v, want nil", got)
	}
}

func TestPassFailLine(t *testing.T) {
	if got := passFailLine(verdict{Pass: true}); got != "pass" {
		t.Errorf("pass line = %q", got)
	}
	if got := passFailLine(verdict{Summary: "broke"}); got != "fail — broke" {
		t.Errorf("fail+summary line = %q", got)
	}
	if got := passFailLine(verdict{}); got != "fail" {
		t.Errorf("bare fail line = %q", got)
	}
}

// --- branch / PR string helpers -------------------------------------------

func TestPrDesc(t *testing.T) {
	tests := map[string]string{
		"feature/COD-529-durable-lessons-memory": "durable lessons memory",
		"fix/COD-570-false-advance":              "false advance",
		"main":                                   "main",
	}
	for branch, want := range tests {
		if got := prDesc(branch); got != want {
			t.Errorf("prDesc(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Add durable lessons memory!": "add-durable-lessons-memory",
		"  Multiple   Spaces  ":       "multiple-spaces",
		"A B C D E F G H":             "a-b-c-d-e-f", // capped at 6 words
		"":                            "",
		"!!!":                         "",
	}
	for in, want := range tests {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrNumber(t *testing.T) {
	if got := prNumber("https://github.com/o/r/pull/272"); got != "272" {
		t.Errorf("prNumber = %q, want 272", got)
	}
}
