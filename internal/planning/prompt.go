package planning

import "strings"

// BuildPrompt is the provider-agnostic planning prompt for a single round. It
// frames the raw idea and the accumulated Q&A transcript (both read fresh from
// disk every round — no agent session is ever resumed), bakes in the /to-prd PRD
// conventions as plain Go text, and specifies the [Payload] output contract.
//
// While capped is false the agent may return one round of structured questions
// or go straight to a PRD; once the round cap is reached capped is true and the
// agent must produce the PRD now, recording any unresolved decisions as explicit
// assumptions in the document.
func BuildPrompt(idea string, transcript []QARound, capped bool) string {
	var b strings.Builder
	b.WriteString(`You are helping turn a raw product idea into a PRD (product requirements document).

Explore the repository to ground the PRD in the actual codebase: use the project's domain vocabulary (CONTEXT.md / glossary) throughout, and respect any ADRs in the area you are touching.

`)
	if capped {
		b.WriteString(`You have already asked the user as many rounds of questions as this session allows. Do NOT ask further questions — you must produce the PRD this round. For anything the user has not settled, take the most reasonable default and record every such choice explicitly under an "## Assumptions" heading in the PRD.

`)
	} else {
		b.WriteString(`If a genuinely blocking decision remains that only the user can make, you may ask ONE round of structured questions (status "questions"). Prefer single- or multi-select questions, each with a sensible default; use free text only when no option set fits. Otherwise synthesize the most reasonable defaults and go straight to the PRD — do not interview the user for things you can decide yourself.

`)
	}

	b.WriteString("The raw idea:\n<idea>\n")
	b.WriteString(strings.TrimSpace(idea))
	b.WriteString("\n</idea>\n")

	if qa := renderTranscript(transcript); qa != "" {
		b.WriteString("\nThe user has already answered these questions in earlier rounds. Treat the answers as settled and do not ask them again:\n<transcript>\n")
		b.WriteString(qa)
		b.WriteString("</transcript>\n")
	}

	b.WriteString(`
Write the PRD as Markdown following this structure:

## Problem Statement
The problem the user faces, from the user's perspective.

## Solution
The solution to that problem, from the user's perspective.

## User Stories
A long, numbered list of user stories in the form "As an <actor>, I want a <feature>, so that <benefit>". Cover all aspects of the feature extensively.

## Implementation Decisions
The modules to build or modify and their interfaces, architectural decisions, schema changes, API contracts, and specific interactions. Do NOT include file paths or code snippets — they go stale.

## Testing Decisions
What makes a good test (test external behavior, not implementation details), which modules will be tested, and prior art for those tests in the codebase.

## Out of Scope
What is deliberately not covered by this PRD.

## Further Notes
Anything else worth recording.

Return your result as a single JSON object — the planning payload.

To deliver the PRD:
{"status":"prd","prd":{"title":"<short title>","markdown":"<the full PRD markdown>"}}
Put the entire PRD in the "markdown" field.
`)

	if !capped {
		b.WriteString(`
To ask the user (only when a decision is genuinely blocking):
{"status":"questions","questions":[{"id":"q1","header":"short chip","text":"the question","kind":"single","options":[{"label":"Option A","description":"..."}],"default":"Option A"}]}
Each question's "kind" is one of "single" (pick one), "multi" (pick any), or "text" (free-form). Give every question a short "header", a "default", and — for single/multi — at least two "options". Do not add "Other" or "skip" options: trau always offers those.
`)
	} else {
		b.WriteString("\nDo not return a \"questions\" payload this round — emit only the \"prd\" object.\n")
	}

	return b.String()
}

// BuildRevisionPrompt is the planning prompt for a PRD revision round. The user
// read the drafted PRD and asked for changes; this round re-reads the idea, the
// settled Q&A, and the current PRD from disk (no agent session is resumed) and
// carries the change note. It must return a revised PRD and never asks questions.
func BuildRevisionPrompt(idea string, transcript []QARound, currentPRD, note string) string {
	var b strings.Builder
	b.WriteString(`You are revising an existing PRD (product requirements document) after the user reviewed it.

Explore the repository to ground the PRD in the actual codebase: use the project's domain vocabulary (CONTEXT.md / glossary) throughout, and respect any ADRs in the area you are touching.

`)

	b.WriteString("The raw idea:\n<idea>\n")
	b.WriteString(strings.TrimSpace(idea))
	b.WriteString("\n</idea>\n")

	if qa := renderTranscript(transcript); qa != "" {
		b.WriteString("\nThe user has already answered these questions in earlier rounds. Treat the answers as settled and do not ask them again:\n<transcript>\n")
		b.WriteString(qa)
		b.WriteString("</transcript>\n")
	}

	b.WriteString("\nThe current PRD draft the user reviewed:\n<prd>\n")
	b.WriteString(strings.TrimSpace(currentPRD))
	b.WriteString("\n</prd>\n")

	b.WriteString("\nThe user read that PRD and requested these changes:\n<changes>\n")
	b.WriteString(strings.TrimSpace(note))
	b.WriteString("\n</changes>\n")

	b.WriteString(`
Revise the PRD to address the requested changes, keeping the rest of the document intact and preserving its structure (Problem Statement, Solution, User Stories, Implementation Decisions, Testing Decisions, Out of Scope, Further Notes). Do NOT ask the user any questions — apply the most reasonable interpretation of the request and record any resulting choices under an "## Assumptions" heading.

Return your result as a single JSON object — the planning payload:
{"status":"prd","prd":{"title":"<short title>","markdown":"<the full revised PRD markdown>"}}
Put the entire revised PRD in the "markdown" field.
`)

	return b.String()
}

// BuildSlicePrompt is the provider-agnostic prompt for the slice round: it frames
// the approved, published PRD (read fresh from disk — no agent session is ever
// resumed), bakes in the /to-issues tracer-bullet vertical-slice conventions as
// plain Go text, and specifies the slices shape of the [Payload] output contract.
// The drafts it returns are reviewed in the TUI before anything is created, so it
// only drafts — it never touches the tracker.
func BuildSlicePrompt(prd string) string {
	var b strings.Builder
	b.WriteString(`You are breaking an approved PRD (product requirements document) into tracer-bullet issues — the draft child issues of the tracker epic that carries the PRD.

Explore the repository to ground the slices in the actual codebase: use the project's domain vocabulary (CONTEXT.md / glossary) in titles and descriptions, and respect any ADRs in the area each slice touches.

The approved PRD:
<prd>
`)
	b.WriteString(strings.TrimSpace(prd))
	b.WriteString(`
</prd>

Break the PRD into tracer-bullet vertical slices:
- Each slice is a thin vertical slice that cuts through ALL integration layers end-to-end (schema, API, UI, tests) — never a horizontal slice of one layer.
- A completed slice is demoable or verifiable on its own.
- Prefer many thin slices over few thick ones.
- Order the slices so every slice comes after the slices it depends on.

Each slice's "description" is Markdown following this structure:

## What to build
A concise description of the vertical slice: the end-to-end behavior, not layer-by-layer implementation. Do NOT include file paths or code snippets — they go stale.

## Acceptance criteria
A "- [ ]" checklist of verifiable criteria.

Each slice's "after" lists the exact titles of the earlier slices it is blocked by — empty when it can start immediately. Leave "labels" empty unless a specific extra tracker label is warranted: trau itself labels every created slice ready and parents it under the epic.

Return your result as a single JSON object — the planning payload:
{"status":"slices","slices":[{"title":"<short descriptive name>","description":"<the markdown above>","labels":[],"after":["<title of an earlier slice>"]}]}
`)
	return b.String()
}

// renderTranscript flattens the accumulated Q&A into plain text the fresh round
// re-reads. Multi-select answers are joined; a skipped answer is flagged as its
// default so the agent knows it was not an explicit choice.
func renderTranscript(rounds []QARound) string {
	var b strings.Builder
	for _, r := range rounds {
		for _, a := range r.Answers {
			q := strings.TrimSpace(a.Question)
			if q == "" {
				q = a.ID
			}
			ans := strings.TrimSpace(strings.Join(a.Values, ", "))
			if ans == "" {
				ans = "(no answer)"
			}
			if a.Skipped {
				ans += " (default — skipped)"
			}
			b.WriteString("Q: " + q + "\nA: " + ans + "\n\n")
		}
	}
	return b.String()
}
