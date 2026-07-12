package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/RomkaLTU/trau/internal/hubclient"
)

// LessonStore is the pipeline's seam for the durable per-repo lessons ledger. Verify
// appends the lesson it distilled; a later build/verify/repair recalls the relevant
// ones. The hub-backed implementation (internal/hublesson) drives it over HTTP so the
// child reads and writes no ledger file, returning the ledger in append order (oldest
// first) so recall selects and tie-breaks exactly as it did against the file. Nil
// disables the ledger — recall injects nothing and records are dropped.
type LessonStore interface {
	Append(l hubclient.Lesson) error
	All() ([]hubclient.Lesson, error)
}

// lesson is one repair-experiment record in the durable lessons ledger: what
// failed, what was tried, the evidence, how it ended, and the distilled takeaway
// a future run should apply. Records accrete append-only under runs/memory/ so a
// failed or repaired run teaches later runs instead of only leaving a transcript
// behind (COD-529). Retrieval is keyed off FailureType/Tags so a phase prompt can
// recall only the records that bear on the work at hand.
type lesson struct {
	Ticket       string   `json:"ticket"`
	Phase        string   `json:"phase"`
	FailureType  string   `json:"failure_type"`
	AttemptedFix string   `json:"attempted_fix"`
	Evidence     []string `json:"evidence,omitempty"`
	Result       string   `json:"result"`
	Lesson       string   `json:"lesson"`
	Tags         []string `json:"tags,omitempty"`
	RecordedAt   string   `json:"recorded_at,omitempty"`
}

const (
	// maxInjectedLessons caps how many distilled lessons a phase prompt recalls,
	// so retrieval stays a hint and never bloats the context.
	maxInjectedLessons = 5
	// maxEvidenceLines caps how many failure lines a record carries as evidence.
	maxEvidenceLines = 8
	// minLessonScore is the relevance floor for retrieval: a tag or failure-type
	// hit (weight 3) or at least two overlapping words clears it, but a single
	// generic word in common does not — keeping recall precise, not noisy.
	minLessonScore = 2

	lessonResultRepaired    = "repaired"
	lessonResultQuarantined = "quarantined"
)

// lessonSchema is the JSON skeleton the opt-in distill agent fills in: just the
// reusable takeaway plus a few keyword tags. The mechanical (free) path never
// touches an agent and synthesizes the same fields in Go.
const lessonSchema = `{"lesson":"<one or two sentence durable takeaway>","tags":["<short-keyword>","..."]}`

// failureCategories maps a coarse failure_type to the substrings that signal it
// in a verdict's failure lines, ordered most-specific first. classifyFailure
// records every category that matches as a retrieval tag and uses the first as
// the record's primary type, so a record can be recalled by any of its facets.
var failureCategories = []struct {
	Type     string
	Keywords []string
}{
	{"migration", []string{"migration", "schema", "foreign key", "column ", "rollback"}},
	{"test", []string{"assert", "expect", "pest", "phpunit", "spec ", "failed asserting", " test"}},
	{"build", []string{"compile", "build failed", "syntax error", "cannot find", "undefined", "unresolved", "import "}},
	{"lint", []string{"lint", "phpstan", "psalm", "gofmt", "go vet", "code style", "formatting"}},
	{"type", []string{"type error", "typeerror", "type mismatch", "expected type", "wrong type"}},
	{"route", []string{"route", " 404", "endpoint", "http ", "request", "response", "status code"}},
	{"timeout", []string{"timed out", "timeout", "deadline"}},
	{"ui", []string{"selector", "browser", "element", "click", "render", "screenshot", "css"}},
	{"data", []string{"null", "nil ", "empty", "missing field", "validation", "constraint"}},
}

// lessonStopwords are generic tokens dropped from query/lesson text so relevance
// scoring keys off meaningful words. Deliberately tiny — over-pruning would hide
// useful matches.
var lessonStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "was": true, "were": true, "not": true, "via": true,
	"from": true, "into": true, "could": true, "should": true, "when": true,
	"failed": true, "failure": true, "verification": true, "verify": true,
	"fix": true, "fixed": true, "quarantined": true, "repaired": true,
}

func lessonDistillPath(id string) string { return "/tmp/lesson-" + id + ".json" }

func (l lesson) wire() hubclient.Lesson {
	return hubclient.Lesson{
		Ticket:       l.Ticket,
		Phase:        l.Phase,
		FailureType:  l.FailureType,
		AttemptedFix: l.AttemptedFix,
		Evidence:     l.Evidence,
		Result:       l.Result,
		Lesson:       l.Lesson,
		Tags:         l.Tags,
		RecordedAt:   l.RecordedAt,
	}
}

func lessonFromWire(w hubclient.Lesson) lesson {
	return lesson{
		Ticket:       w.Ticket,
		Phase:        w.Phase,
		FailureType:  w.FailureType,
		AttemptedFix: w.AttemptedFix,
		Evidence:     w.Evidence,
		Result:       w.Result,
		Lesson:       w.Lesson,
		Tags:         w.Tags,
		RecordedAt:   w.RecordedAt,
	}
}

// classifyFailure derives a coarse failure_type plus retrieval tags from a
// verdict. The type is the first category whose keywords appear in the failure
// text; tags are every category that matched. An unclassifiable failure types as
// "other" with no tags.
func classifyFailure(v verdict) (failureType string, tags []string) {
	hay := strings.ToLower(strings.Join(v.Failures, " ") + " " + v.Summary)
	for _, c := range failureCategories {
		for _, kw := range c.Keywords {
			if strings.Contains(hay, kw) {
				tags = append(tags, c.Type)
				break
			}
		}
	}
	if len(tags) == 0 {
		return "other", nil
	}
	return tags[0], tags
}

// newLesson assembles a repair-experiment record from the data the verify path
// already holds. The distilled lesson is synthesized in Go (free, deterministic);
// recordLesson optionally enriches it via the opt-in distill agent.
func newLesson(id, phase, attemptedFix, result string, v verdict) lesson {
	ftype, tags := classifyFailure(v)
	ev := v.Failures
	if len(ev) > maxEvidenceLines {
		ev = ev[:maxEvidenceLines]
	}
	return lesson{
		Ticket:       id,
		Phase:        phase,
		FailureType:  ftype,
		AttemptedFix: attemptedFix,
		Evidence:     append([]string(nil), ev...),
		Result:       result,
		Lesson:       mechanicalLesson(ftype, result, attemptedFix, v),
		Tags:         tags,
	}
}

// mechanicalLesson condenses the verdict into a one-line takeaway with no agent
// call — the default distillation. It states the failure type, how it ended, and
// the verdict summary so even the free path leaves a recall-able record.
func mechanicalLesson(ftype, result, attemptedFix string, v verdict) string {
	sum := strings.TrimSpace(v.Summary)
	if sum == "" && len(v.Failures) > 0 {
		sum = strings.TrimSpace(v.Failures[0])
	}
	if sum == "" {
		sum = "verification failed"
	}
	if result == lessonResultRepaired {
		return ftype + " failure fixed by " + attemptedFix + ": " + sum
	}
	return ftype + " failure not fixed by " + attemptedFix + " (quarantined): " + sum
}

// attemptLabel names the kind of fix a verify run applied, for the record's
// attempted_fix field.
func attemptLabel(repairs, bugfixes int) string {
	switch {
	case repairs > 0 && bugfixes > 0:
		return "repair+bugfix"
	case bugfixes > 0:
		return "bugfix"
	case repairs > 0:
		return "repair"
	default:
		return "none"
	}
}

// recordLesson distills a repair experiment into a durable lesson and appends it
// to the ledger. The lesson text is synthesized in Go by default; when
// LessonsDistill is set it first runs a cheap, isolated agent pass to write a
// richer takeaway, merged in when present. Best-effort throughout: a no-op when
// lessons are disabled, and a distill failure simply keeps the mechanical record.
func (p *Pipeline) recordLesson(ctx context.Context, id string, v verdict, attemptedFix, result string) {
	if !p.Lessons {
		return
	}
	l := newLesson(id, "verify", attemptedFix, result, v)
	if p.LessonsDistill {
		if distilled, tags, ok := p.distillLesson(ctx, id, l); ok {
			l.Lesson = distilled
			l.Tags = mergeTags(l.Tags, tags)
		}
	}
	l.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	p.appendLesson(l)
	p.logf("  ↳ lesson recorded (%s/%s): %s", l.FailureType, result, truncateLesson(l.Lesson, 80))
}

// distillInstruction asks an isolated agent to distill the single most reusable,
// ticket-agnostic lesson from a finished repair experiment and write it as JSON.
func distillInstruction(id, result, ftype, evidence, path string) string {
	return "A repair experiment for " + id + " just ended (" + result + "; failure type: " + ftype + "). Evidence:\n" + evidence +
		"\n\nDistill the single most reusable lesson a FUTURE run on similar work should remember to avoid or fix this faster. One or two sentences, concrete and general — no ticket-specific identifiers, file paths, or IDs. Also give 1-4 short lowercase keyword tags. Write ONLY this JSON to exactly " + path + " (overwrite if present) and nowhere else: " + lessonSchema + ". Do not change any code, run tests, commit, push, or open a PR."
}

// distillLesson runs the opt-in distillation agent and returns its richer lesson
// + tags. It calls the runner directly (not the budget-guarded phase path) so a
// post-hoc enrichment can never quarantine a ticket that already finished its real
// work; any error or missing/!valid output reads as "no distillation" so the
// mechanical record stands.
func (p *Pipeline) distillLesson(ctx context.Context, id string, l lesson) (string, []string, bool) {
	path := lessonDistillPath(id)
	_ = os.Remove(path)
	evidence := strings.Join(l.Evidence, "\n")
	if strings.TrimSpace(evidence) == "" {
		evidence = l.Lesson
	}
	if _, err := p.agentPhaseOn(ctx, id, "distill", distillInstruction(id, l.Result, l.FailureType, evidence, path), p.Runner); err != nil {
		return "", nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, false
	}
	var out struct {
		Lesson string   `json:"lesson"`
		Tags   []string `json:"tags"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", nil, false
	}
	if strings.TrimSpace(out.Lesson) == "" {
		return "", nil, false
	}
	return strings.TrimSpace(out.Lesson), out.Tags, true
}

// appendLesson records one distilled lesson in the repo's durable ledger through
// the hub. Best-effort and silent: a write failure never blocks the loop — the
// ledger is an optimization, not a checkpoint, and a dropped record is re-produced
// the next time a similar failure recurs.
func (p *Pipeline) appendLesson(l lesson) {
	if !p.Lessons || p.LessonLedger == nil {
		return
	}
	_ = p.LessonLedger.Append(l.wire())
}

// recallLessons reads the ledger and returns the distilled lesson lines relevant
// to query, capped at maxInjectedLessons. A no-op (nil) when lessons are disabled.
func (p *Pipeline) recallLessons(query string) []string {
	if !p.Lessons || p.LessonLedger == nil {
		return nil
	}
	return relevantLessons(p.ledger(), query, maxInjectedLessons)
}

// ledger reads the repo's recorded lessons from the hub in append order (oldest
// first), the order the relevance scan was tuned against, so recall selects and
// tie-breaks identically to the file era. A hub error reads as an empty ledger — the
// loop simply recalls nothing rather than blocking.
func (p *Pipeline) ledger() []lesson {
	wire, err := p.LessonLedger.All()
	if err != nil {
		return nil
	}
	out := make([]lesson, len(wire))
	for i, w := range wire {
		out[i] = lessonFromWire(w)
	}
	return out
}

// lessonQuery is the relevance key for the build/verify phases: the ticket title
// (its domain) plus the id. An empty title degrades to the id alone, which rarely
// matches — so an early run with a thin ledger injects nothing.
func (p *Pipeline) lessonQuery(id string) string {
	return strings.TrimSpace(p.State.Get(id, "TITLE") + " " + id)
}

// relevantLessons returns up to max distilled lesson lines that match query, most
// relevant first, so a phase prompt recalls only what bears on the work at hand.
// The query is free text — a ticket title, or the current failure lines — scored
// by tag / failure-type / word overlap; later (more recent) records win ties.
// Returns nil when the ledger is empty or nothing is relevant, so callers inject
// nothing rather than a dangling note.
func relevantLessons(lessons []lesson, query string, max int) []string {
	if len(lessons) == 0 || max <= 0 {
		return nil
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		idx    int
		score  int
		lesson string
	}
	var matches []scored
	for i, l := range lessons {
		if s := lessonScore(l, terms); s >= minLessonScore {
			matches = append(matches, scored{idx: i, score: s, lesson: strings.TrimSpace(l.Lesson)})
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.SliceStable(matches, func(a, b int) bool {
		if matches[a].score != matches[b].score {
			return matches[a].score > matches[b].score
		}
		return matches[a].idx > matches[b].idx
	})
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if m.lesson == "" || seen[m.lesson] {
			continue
		}
		seen[m.lesson] = true
		out = append(out, m.lesson)
		if len(out) >= max {
			break
		}
	}
	return out
}

// lessonScore ranks a record against the query terms: tags and the primary
// failure type weigh heaviest (the retrieval keys), with a lighter signal from
// distilled-text word overlap.
func lessonScore(l lesson, terms map[string]bool) int {
	score := 0
	for _, t := range l.Tags {
		if terms[strings.ToLower(t)] {
			score += 3
		}
	}
	if terms[strings.ToLower(l.FailureType)] {
		score += 3
	}
	for _, w := range tokenize(l.Lesson) {
		if terms[w] {
			score++
		}
	}
	return score
}

func queryTerms(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range tokenize(s) {
		m[t] = true
	}
	return m
}

// tokenize lowercases s and splits it into meaningful word tokens (alphanumeric,
// length ≥ 3, non-stopword) for relevance matching.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isWordRune(r)
	})
	var out []string
	for _, f := range fields {
		if len(f) < 3 || lessonStopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

func mergeTags(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, t := range append(append([]string{}, a...), b...) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func truncateLesson(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// buildLessonsNote renders recalled lessons for a build/handoff prompt: prior
// pitfalls to avoid. Empty when none are relevant.
func buildLessonsNote(lessons []string) string {
	if len(lessons) == 0 {
		return ""
	}
	return " Lessons from earlier runs on similar work (apply if relevant, ignore if not): " + joinLessons(lessons) + "."
}

// verifyLessonsNote points the cold verifier at failure modes earlier runs hit on
// similar slices — sharpening the adversarial pass without leaking this run's
// build reasoning. Empty when none are relevant.
func verifyLessonsNote(lessons []string) string {
	if len(lessons) == 0 {
		return ""
	}
	return " Failure modes earlier runs hit on similar slices — check these specifically: " + joinLessons(lessons) + "."
}

// repairLessonsNote points a repair/bugfix pass at fixes earlier runs recorded for
// similar failures. Empty when none are relevant.
func repairLessonsNote(lessons []string) string {
	if len(lessons) == 0 {
		return ""
	}
	return " Earlier runs recorded these fixes for similar failures (apply if they fit): " + joinLessons(lessons) + "."
}

func joinLessons(lessons []string) string {
	return strings.Join(lessons, " | ")
}
