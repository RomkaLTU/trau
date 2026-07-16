# ADR 0013 — Atlas Views are validated JSON graph documents, not Mermaid

- **Status:** Accepted
- **Date:** 2026-07-16
- **Deciders:** Romas (sole maintainer)
- **Ticket:** COD-952 (epic COD-951)

## Context

The Atlas is a per-repo web page of agent-generated architecture Views
(`CONTEXT.md` — **Atlas**, **View**): day one a **Data model** (entities and
their relationships) and **App flows** (the significant runtime flows, each its
own small graph). A View is produced by an agent reading the repo at a stamped
commit and has to render interactively on a big screen — pannable, zoomable,
styled in the design system, legible at thirty-plus nodes.

Two contracts were on the table for what the agent emits and the hub stores:

- **Mermaid text.** The most LLM-native diagram format — models produce it
  fluently and it is trivial to store as a blob. But the renderer is a static,
  weakly interactive SVG that degrades past ~30 nodes, node identity is a
  by-product of source-line order rather than a stable key, and it is styled by
  Mermaid's themer, not ours. Regenerating a diagram reshuffles it wholesale.
- **A structured JSON graph document** conforming to a per-View schema, rendered
  by React Flow with elkjs layout and design-system-styled custom nodes. Costs
  us a schema contract and server-side validation, buys interactivity, styling,
  and stable node identity across regenerations.

## Decision

Agents emit **JSON conforming to a per-View schema**; the hub validates the
document before storing it; the web renders it with React Flow + elkjs and
custom nodes. **Mermaid is never stored.**

- The View catalog is generic — a View is `{id, title, prompt, schema flavor}`.
  Day one: `data-model` and `app-flows`. Prompts land in the runner slice; the
  catalog carries id/title/flavor.
- **Stable-ID rule, shared by both flavors:** node ids are kebab-case slugs
  derived from the concept name (`User` → `user`, `POST /checkout` →
  `post-checkout`), so a regenerated document keeps node identity and the
  renderer can animate a diff instead of replacing the graph.
- The hub validates on the way in: unknown fields, dangling endpoint/edge
  references, duplicate ids, out-of-range enums, and empty documents are
  rejected. Invalid generator output is retried once (in the runner slice); a
  second failure stores an error row without displacing the last good document.
- The latest **valid** document per (repo, View) surfaces. History is retained
  ten deep per (repo, View).
- Staleness is derived from run data the hub already has — the count of merged
  runs recorded after the latest good document's generated-at — never git
  polling.

## Consequences

- Interactive, styleable, big-screen-worthy rendering with stable node identity
  across regenerations.
- In exchange we own the schema contract, server-side validation with one
  generation retry on invalid output, and layout tuning.
- Renderer and generator evolve independently as long as the document contract
  holds: the web can restyle nodes or swap the layout engine, and the generator
  can change its prompt, without a migration — only a schema change touches both.
- The contract, the hub store, and the read API are this slice (COD-952);
  generation (the runner, prompts, and the retry) and the web rendering are
  later slices in COD-951.
