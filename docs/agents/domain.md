# Domain Docs

This is a single-context repository.

## Before exploring

- Read `CONTEXT.md` at the repo root.
- Read the ADRs under `docs/adr/` that touch the area being changed.

If either resource is absent, proceed silently. Domain documentation is created or extended only when an actual terminology or architectural decision requires it.

## Use the glossary vocabulary

Use the terms defined in `CONTEXT.md` in issue titles, descriptions, code, tests, and documentation. In particular, preserve the distinctions between Provider, Model, Route, Fallback provider, and Provider override, and avoid the near-synonyms the glossary rejects.

If a required concept is missing from the glossary, reconsider whether the new term is necessary or flag the gap for a documentation decision.

## Flag ADR conflicts

If proposed work contradicts an existing ADR, surface the conflict explicitly rather than silently overriding the decision.
