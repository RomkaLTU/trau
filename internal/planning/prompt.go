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
