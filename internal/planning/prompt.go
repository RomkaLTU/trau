package planning

import "strings"

// BuildPrompt is the provider-agnostic planning prompt for a single round. It
// frames the raw idea, bakes in the /to-prd PRD conventions as plain Go text (so
// the module never depends on a repo skill being present), and specifies the
// [Payload] output contract. This slice instructs the agent to go straight to a
// PRD — the question round is a later slice — but the payload contract already
// documents all three statuses so the protocol is stable.
func BuildPrompt(idea string) string {
	var b strings.Builder
	b.WriteString(`You are helping turn a raw product idea into a PRD (product requirements document).

Explore the repository to ground the PRD in the actual codebase: use the project's domain vocabulary (CONTEXT.md / glossary) throughout, and respect any ADRs in the area you are touching. Do NOT interview the user in prose — synthesize what you can from the idea and the code, and take the most reasonable default for anything ambiguous.

Go straight to a PRD this round. Do not return questions.

The raw idea:
<idea>
`)
	b.WriteString(strings.TrimSpace(idea))
	b.WriteString(`
</idea>

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

Return your result as a single JSON object — the planning payload:

{"status":"prd","prd":{"title":"<short title>","markdown":"<the full PRD markdown>"}}

The payload protocol also defines two other statuses you are NOT to use this round:
- {"status":"questions","questions":[{"id":"q1","header":"short chip","text":"…","options":[{"label":"…","description":"…"}],"multi":false,"allow_other":true,"default":"…"}]}
- {"status":"slices","slices":[{"title":"…","description":"…","labels":[],"after":[]}]}

Emit only the JSON object for status "prd". Put the entire PRD in the "markdown" field.`)
	return b.String()
}
