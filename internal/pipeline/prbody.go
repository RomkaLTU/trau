package pipeline

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/RomkaLTU/trau/internal/sanitize"
)

// prBody assembles the slice PR description from the run's own recorded results:
// the summary captured from the build phase, the verify verdict and rubric on
// disk (or their durable hub copies), and a trailer naming the tracker that
// actually owns the ticket. Every line states what happened — nothing is
// pre-checked, and no attribution of any kind is added.
func (p *Pipeline) prBody(ctx context.Context, id string) string {
	var b strings.Builder
	b.WriteString("## Summary\n")
	b.WriteString(p.prSummary(ctx, id))
	b.WriteString("\n\n## Testing\n")
	for _, line := range p.testingLines(ctx, id) {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n" + p.ticketRef(id) + "\n")
	return b.String()
}

func (p *Pipeline) epicPRBody(id string) string {
	return "## Summary\nEpic integration branch for " + id + ". Features land on the epic branch first; this PR ships the epic to " + p.Base + " once complete.\n\n" + p.ticketRef(id) + "\n"
}

// ticketRef is the PR body's ticket trailer, named for the tracker that owns the
// id: an internal issue — the internal provider, or an id carrying the repo's
// internal prefix on a synced repo — is a plain Ref, since no external tracker
// knows it. Unknown providers (github, unwired tests) stay a neutral Ref too.
func (p *Pipeline) ticketRef(id string) string {
	pfx, _, _ := strings.Cut(id, "-")
	if p.TrackerProvider == "internal" || (p.InternalPrefix != "" && strings.EqualFold(pfx, p.InternalPrefix)) {
		return "Ref: " + id
	}
	switch p.TrackerProvider {
	case "jira":
		return "Jira: " + id
	case "linear":
		return "Linear: " + id
	default:
		return "Ref: " + id
	}
}

// prSummary is the Summary section body: the sentences captured from the build
// phase output, or a neutral line derived from the ticket title when the build
// left none behind.
func (p *Pipeline) prSummary(ctx context.Context, id string) string {
	if s := strings.TrimSpace(p.State.Get(id, "BUILD_SUMMARY")); s != "" {
		return s
	}
	title, _ := p.Tracker.Title(ctx, id)
	title = strings.TrimRight(strings.TrimSpace(title), ".")
	if title == "" {
		return "Implements " + id + "."
	}
	return "Implements " + id + ": " + title + "."
}

// testingLines renders the Testing section facts: the required tests with the
// graded result, the verify checks that ran, and the browser-QA outcome from the
// verdict's accounting. With no verdict on record it says exactly that.
func (p *Pipeline) testingLines(ctx context.Context, id string) []string {
	v, graded := p.sliceVerdict(id)
	if !graded {
		return []string{"No verify verdict was recorded for this run"}
	}
	result := "passed"
	if !v.Pass {
		result = "failed"
	}
	var lines []string
	if tests := p.requiredTests(id); len(tests) > 0 {
		lines = append(lines, "Tests: "+strings.Join(tests, "; ")+" — "+result)
	} else if s := sanitize.FeedLine(v.Summary); s != "" {
		lines = append(lines, "Verify "+result+": "+s)
	} else {
		lines = append(lines, "Verify "+result)
	}
	if len(v.Checks) > 0 {
		lines = append(lines, "Verify checks: "+checkResultsLine(v.Checks))
	}
	lines = append(lines, "Browser QA: "+browserLine(v, p.sliceAppURL(ctx)))
	return lines
}

// sliceVerdict reads the run's graded verdict — /tmp first, then the durable hub
// copy for a resume whose /tmp was wiped.
func (p *Pipeline) sliceVerdict(id string) (verdict, bool) {
	if v, ok := readVerdict(verifyPath(id)); ok {
		return v, true
	}
	content, ok := p.getArtifact(id, artifactVerdict)
	if !ok {
		return verdict{}, false
	}
	var v verdict
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return verdict{}, false
	}
	return v, true
}

func (p *Pipeline) requiredTests(id string) []string {
	path, ok := p.activeRubric(id)
	if !ok {
		return nil
	}
	r, _ := readRubric(path)
	var tests []string
	for _, cmd := range r.RequiredTests {
		if s := sanitize.FeedLine(cmd); s != "" {
			tests = append(tests, s)
		}
	}
	return tests
}

func checkResultsLine(results []checkResult) string {
	parts := make([]string, 0, len(results))
	for _, c := range results {
		status := "passed"
		if !c.Pass {
			status = "failed"
		}
		parts = append(parts, c.Name+" "+status)
	}
	return strings.Join(parts, ", ")
}

// browserLine states the browser-QA fact from the verdict's accounting field —
// browserOutcome already reads an absent value as skipped, so an old or evasive
// verdict can never yield a browser claim here.
func browserLine(v verdict, appURL string) string {
	switch browserOutcome(v) {
	case "driven":
		if appURL != "" {
			return "driven against " + appURL
		}
		return "driven in a real browser"
	case "not-applicable":
		return "not applicable — backend-only slice"
	default:
		if notes := sanitize.FeedLine(v.BrowserNotes); notes != "" {
			return "not run — " + notes
		}
		return "not run"
	}
}

// attributionRE marks build-output sentences the PR body must never carry: any
// mention of the loop, an AI/agent, or automation. Over-matching is fine — a
// rejected capture just falls back to the title-derived summary.
var attributionRE = regexp.MustCompile(`(?i)\b(trau|claude|codex|gemini|copilot|ai|llm|agent|bot|assistant|automated|automation|autonomous)\b|loop`)

const buildSummaryMax = 480

// buildResultProseKeys are the fields a JSON build result may carry its prose
// summary in, most specific first.
var buildResultProseKeys = []string{"summary", "description", "result", "notes"}

// unwrapBuildResult reports whether the build result is a JSON object and, if so,
// the prose it carries. The agent interface invites a result object, and one
// flattened into a paragraph reads as a wall of braces and quotes, so an object
// carrying no prose field yields "" and the caller falls back to the ticket title
// rather than shipping the fields themselves.
func unwrapBuildResult(out string) (string, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		return "", false
	}
	for _, key := range buildResultProseKeys {
		var prose string
		if json.Unmarshal(obj[key], &prose) == nil && strings.TrimSpace(prose) != "" {
			return prose, true
		}
	}
	return "", true
}

// summarizeBuildOutput distills the build agent's final message into the 1–3
// sentence PR summary: the first prose paragraph, skipping headings, lists,
// fences, and tables. A JSON result is unwrapped to its prose first. A paragraph
// tripping attributionRE yields "" outright so the caller falls back instead of
// shipping any attribution.
func summarizeBuildOutput(out string) string {
	if prose, isJSON := unwrapBuildResult(out); isJSON {
		out = prose
	}
	for _, para := range strings.Split(out, "\n\n") {
		text := proseParagraph(para)
		if text == "" {
			continue
		}
		if attributionRE.MatchString(text) {
			return ""
		}
		return firstSentences(text, 3)
	}
	return ""
}

// proseParagraph flattens a paragraph to one line, or "" when any of its lines
// is markdown or JSON structure rather than prose.
func proseParagraph(para string) string {
	var lines []string
	for _, ln := range strings.Split(para, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		switch ln[0] {
		case '#', '-', '*', '>', '|', '`', '{', '[':
			return ""
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, " ")
}

func firstSentences(text string, max int) string {
	count, end := 0, len(text)
	for i := 0; i+1 < len(text); i++ {
		switch text[i] {
		case '.', '!', '?':
			if text[i+1] == ' ' {
				count++
				if count == max {
					end = i + 1
				}
			}
		}
		if count == max {
			break
		}
	}
	s := strings.TrimSpace(text[:end])
	if len(s) > buildSummaryMax {
		if cut := strings.LastIndexByte(s[:buildSummaryMax], ' '); cut > 0 {
			s = s[:cut]
		} else {
			s = s[:buildSummaryMax]
		}
		s += "…"
	}
	return s
}
