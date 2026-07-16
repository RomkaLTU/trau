package hubatlas

import "strings"

// sharedPreamble opens every View's generation prompt: the read-only contract, the
// bare-JSON output rule, the stable-ID rule, and how to treat a prior document on a
// regeneration (ADR 0013). Per-View instructions and the flavor schema follow it.
const sharedPreamble = `You are generating one architecture View of this repository.

- You are read-only: explore the repository, never modify, build, or run anything with side effects.
- Output exactly one JSON document conforming to the provided schema — no prose, no markdown fences.
- Node ids are kebab-case slugs derived from concept names.
- If a previous document is provided, re-derive everything from the code, but reuse its ids and names for concepts that still exist. Never copy content you cannot confirm in the code.`

// dataModelPrompt is the Data model View's curated instruction body.
const dataModelPrompt = `Produce the Data model View: the repository's persistence entities and how they relate.

Locate the persistence source of truth in priority order — migrations, schema files, ORM/model definitions. Emit entities with typed fields and their primary keys flagged, relationships with a cardinality (1:1, 1:N, or N:M), and a domain grouping key per entity. Describe only what the code proves — never invent tables or fields.`

// appFlowsPrompt is the App flows View's curated instruction body.
const appFlowsPrompt = `Produce the App flows View: the significant runtime flows that define what this application does.

Identify the 3 to 8 flows that define what this application does — each an entry point running through the layers to its side effects. Every flow is a named small graph of steps typed by kind (ui, http, service, job, queue, db, external, other) with a one-sentence summary. Prefer breadth of significant flows over exhaustive depth of any one flow.`

// schemaDoc describes the exact JSON shape a flavor's document must take, so the
// agent emits fields the validators accept rather than guessing at the contract.
func schemaDoc(f Flavor) string {
	switch f {
	case FlavorDataModel:
		return `Schema — a single JSON object:
{
  "entities": [
    {
      "id": "<kebab-case slug>",
      "name": "<display name>",
      "domain": "<grouping key>",
      "fields": [ { "name": "<field name>", "type": "<type>", "pk": <true|false> } ]
    }
  ],
  "relationships": [
    {
      "id": "<kebab-case slug>",
      "from": "<entity id>",
      "to": "<entity id>",
      "cardinality": "1:1" | "1:N" | "N:M",
      "label": "<short label>"
    }
  ]
}
At least one entity is required. Every relationship's from and to must reference an entity id.`
	case FlavorAppFlows:
		return `Schema — a single JSON object:
{
  "flows": [
    {
      "id": "<kebab-case slug>",
      "name": "<display name>",
      "summary": "<one sentence>",
      "steps": [ { "id": "<kebab-case slug>", "name": "<display name>", "kind": "ui|http|service|job|queue|db|external|other" } ],
      "edges": [ { "from": "<step id>", "to": "<step id>", "label": "<short label>" } ]
    }
  ]
}
Between 1 and 8 flows. Every edge's from and to must reference a step id within the same flow.`
	default:
		return ""
	}
}

// GenerationPrompt is the first-attempt prompt for v: the shared preamble, v's
// curated instructions, the flavor schema, and the prior document to reuse ids from
// when one exists. previous is the last good document JSON, or empty for a first run.
func (v View) GenerationPrompt(previous string) string {
	return v.composePrompt(previous, "", "")
}

// RetryPrompt is the second-attempt prompt after invalid output: the generation
// prompt plus the rejected output and the reason it failed validation, so the agent
// corrects the specific contract violation rather than starting blind.
func (v View) RetryPrompt(previous, rejected, reason string) string {
	return v.composePrompt(previous, rejected, reason)
}

func (v View) composePrompt(previous, rejected, reason string) string {
	var b strings.Builder
	b.WriteString(sharedPreamble)
	b.WriteString("\n\n")
	b.WriteString(v.Prompt)
	b.WriteString("\n\n")
	b.WriteString(schemaDoc(v.Flavor))
	if previous != "" {
		b.WriteString("\n\nPrevious document (reuse ids and names for concepts that still exist):\n")
		b.WriteString(previous)
	}
	if reason != "" {
		b.WriteString("\n\nYour previous attempt was rejected: ")
		b.WriteString(reason)
		if rejected != "" {
			b.WriteString("\nThe rejected output was:\n")
			b.WriteString(rejected)
		}
		b.WriteString("\nFix the issue and output a corrected JSON document.")
	}
	return b.String()
}
