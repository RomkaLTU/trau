# Trau Web — V0 Prompt Kit

**Version:** v1 (2026-07-05)
**Epic:** [COD-720](https://linear.app/codesomelabs/issue/COD-720) — trau web improvements (trau.sh design language + CLI parity)
**Design source of truth:** the trau.sh marketing site (`~/Projects/trau-web`), adopted verbatim and dark-only.

This is the shared preamble for every screen-design prompt in the web overhaul. Each screen is designed by
running **this preamble + one screen spec** through [V0](https://v0.dev). Generated designs land in the separate
UI repo **[github.com/RomkaLTU/trau-cli-web](https://github.com/RomkaLTU/trau-cli-web)**; the wiring slices then
adapt each design into the embedded SPA (`web/`) in this repo.

Two audiences read this document:

- **V0** (the generator) — reads the [Preamble](#preamble) block. Everything it needs to design one on-brand
  screen is in that block; nothing else is required.
- **Wiring engineers** — read the [Assembling a screen prompt](#assembling-a-screen-prompt),
  [Generation workflow](#generation-workflow), and [Next.js → Vite adaptation notes](#nextjs--vite-adaptation-notes)
  sections when porting a V0 design into `web/`.

---

## Assembling a screen prompt

A complete V0 prompt is exactly two parts, concatenated:

```
<the Preamble block below, verbatim>

## Screen: <name>
<the screen spec>
```

The preamble is self-contained — a screen spec never needs to restate tokens, fonts, components, palette, or tone.
A screen spec supplies only what is specific to that screen:

- **Purpose** — one line on what the screen is for.
- **Layout** — regions, hierarchy, what dominates.
- **Content** — the data shown, with realistic placeholder values (real ticket ids like `COD-738`, real repo
  names, plausible run states).
- **States** — empty, loading, and at least one error/fault variant where relevant.
- **Signature components** — which of the recipes below appear, and where.

Keep the screen spec declarative. Do not re-specify colors as hex — name the semantic role (`active`, `fault`,
`paused`) and let the palette mapping resolve it.

---

## Preamble

> Everything between the markers is the reusable preamble. Copy it verbatim to the top of every screen prompt.

<!-- ===== BEGIN PREAMBLE ===== -->

You are designing one screen of **trau**, a terminal-native web dashboard for an autonomous coding agent that
picks tickets, runs an implement→verify→merge pipeline, and loops. The aesthetic is a **warm, dark, terminal**
surface — soil and ember, not neon cyberpunk. Restrained, functional, and legible under long observation.
Design **one screen**, dark-only, using the tokens, fonts, components, palette, and tone below and nothing else.

### Design tokens

Dark-only. Use these CSS custom properties as the single source of color and radius. Do not invent colors; every
surface, text, and accent must resolve to a token below. In Tailwind, prefer the token utilities
(`bg-card`, `text-foreground`, `border-border`, `text-teal`, `bg-primary`) over raw hex.

```css
:root {
  color-scheme: dark;

  --background: #0c0a09;   /* app canvas — warm near-black soil */
  --foreground: #efe9e3;   /* primary text — warm bone */
  --card: #14110e;         /* raised surface */
  --card-foreground: #efe9e3;
  --popover: #181410;
  --popover-foreground: #efe9e3;
  --primary: #ff7a18;      /* brand ember-orange — primary action, wordmark, accent */
  --primary-foreground: #1a1004;
  --secondary: #1f1a15;
  --secondary-foreground: #efe9e3;
  --muted: #1a1611;
  --muted-foreground: #9b9085; /* secondary/label text */
  --accent: #1f1a15;
  --accent-foreground: #efe9e3;
  --destructive: #ff4672;
  --border: #322a22;       /* hairline borders on the warm dark */
  --input: #1f1a15;
  --ring: #ff7a18;

  /* Semantic run-state palette (from the TUI) */
  --brand: #ff7a18;
  --teal:  #00d4aa;
  --done:  #04b575;
  --fail:  #ff4672;
  --warn:  #f9d423;
  --info:  #00aaff;
  --faint: #555555;

  --radius: 0.875rem;      /* lg; sm/md derive as lg−4px / lg−2px, xl as lg+4px */
}
```

### Fonts

- **Space Grotesk** — headings and body prose. The default UI typeface.
- **Geist Mono** — labels, code, buttons, eyebrow tags, ticket ids, run states, metrics, timestamps, filenames,
  terminal output. Anything machine-flavored is mono.

Rule of thumb: if it names a control, a state, an id, or a number, it is **Geist Mono**. If it reads as a
sentence, it is **Space Grotesk**.

### Semantic palette → run-state mapping

Every run state maps to exactly one semantic color. Use these consistently across pills, glyphs, borders, and
chart series:

| Token       | Hex       | Run state / meaning                               |
| ----------- | --------- | ------------------------------------------------- |
| `teal`      | `#00d4aa` | **active** — running, in progress                 |
| `done`      | `#04b575` | **success** — merged, verified, complete          |
| `fail`      | `#ff4672` | **failure** — verify failure, fault, quarantine   |
| `warn`      | `#f9d423` | **attention** — paused, needs input, warning      |
| `info`      | `#00aaff` | **neutral** — informational, in-between           |
| `faint`     | `#555555` | **idle** — todo, queued, disabled, not started    |
| `brand`     | `#ff7a18` | primary action, wordmark, brand accent (not a state) |

### Signature components

Reproduce these recipes wherever the screen spec calls for them. They are what makes a screen read as trau.

**Terminal-window card.** The default container for a discrete unit of content. A card
(`bg-card`, `border border-border`, `rounded-lg`) with a **title bar**: a row holding three traffic-light dots
(≈10px circles in `fail` / `warn` / `done`) on the left and a **Geist Mono** title on the right or center
(a filename-ish label like `overview`, `run.log`, `costs.tsx`). The body sits below a hairline divider. Optional:
a faint inner `scanlines` texture on the body.

**Semantic status pill.** A small bordered, translucent-tint chip for a run state. Border in the semantic color,
background as a low-opacity tint of the same color, text in the same color, **Geist Mono**, compact. E.g. active →
`border-teal/60 bg-teal/12 text-teal`; fault → `border-fail/60 bg-fail/12 text-fail`. Prefix with the matching
state glyph (below).

**Mono eyebrow label.** A small uppercase **Geist Mono** kicker above a heading or section, letter-spaced, usually
`muted-foreground` or a semantic color, prefixed with a glyph that signals state:

- `▸` action / start / current
- `●` active / live
- `○` idle / todo
- `✓` done / success
- `⚠` attention / warning
- `◔` in progress / partial

**Blinking block cursor.** A block glyph (`▍`) that blinks (≈1.1s step-end), used as an accent after the wordmark,
a section heading, or a live/streaming label. Guard the blink under `prefers-reduced-motion: reduce` (steady, no
animation).

**Furrow-grid backdrop.** A faint dotted radial-gradient grid (soil furrows) used behind empty states and hero
regions. Pair with a warm ember/teal radial glow for emphasis.

**Texture layers (optional, decorative).** Film grain and scanline overlays add tactility. They are optional and
must never reduce legibility; any animated texture is guarded under `prefers-reduced-motion: reduce`. Keep them
subtle — this is a tool for long observation, not a demo reel.

Reference CSS for the signature textures and animations (from trau.sh, verbatim):

```css
/* Furrow / dotted grid background — empty states, heroes */
.furrow-grid {
  background-image: radial-gradient(circle, rgba(255, 122, 24, 0.11) 1px, transparent 1px);
  background-size: 28px 28px;
}

/* Warm ember + teal radial glow */
.hero-glow {
  background:
    radial-gradient(ellipse 60% 50% at 50% 0%, rgba(255, 122, 24, 0.16), transparent 70%),
    radial-gradient(ellipse 40% 40% at 80% 20%, rgba(0, 212, 170, 0.08), transparent 70%);
}

/* Blinking block cursor */
@keyframes blink { 0%, 49% { opacity: 1; } 50%, 100% { opacity: 0; } }
.cursor-block { animation: blink 1.1s step-end infinite; }

/* Scanline texture — subtle, optional */
.scanlines::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: repeating-linear-gradient(
    to bottom,
    rgba(255, 255, 255, 0.015) 0px,
    rgba(255, 255, 255, 0.015) 1px,
    transparent 1px,
    transparent 3px
  );
}

/* Page texture: furrow ridges + film grain (apply to body via ::before / ::after) */
body::before {
  content: '';
  position: fixed; inset: 0; z-index: 0; pointer-events: none;
  background-image:
    radial-gradient(ellipse 120% 80% at 50% -10%, rgba(255, 122, 24, 0.07), transparent 55%),
    radial-gradient(ellipse 100% 90% at 100% 100%, rgba(0, 212, 170, 0.04), transparent 60%),
    repeating-linear-gradient(115deg,
      rgba(255, 138, 51, 0.035) 0px, rgba(255, 138, 51, 0.035) 1px,
      transparent 1px, transparent 22px),
    repeating-linear-gradient(115deg,
      rgba(0, 0, 0, 0.18) 0px, rgba(0, 0, 0, 0.18) 11px,
      transparent 11px, transparent 22px);
}
body::after {
  content: '';
  position: fixed; inset: 0; z-index: 0; pointer-events: none;
  opacity: 0.5; mix-blend-mode: overlay;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='180' height='180'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.85' numOctaves='3' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23n)' opacity='0.55'/%3E%3C/svg%3E");
  background-size: 180px 180px;
}

@media (prefers-reduced-motion: reduce) {
  .cursor-block { animation: none !important; }
}
```

### Copy tone

- **Controls are functional.** Buttons, labels, states, and headings are plain and imperative: `Run once`,
  `Loop`, `Stop`, `Resume`, `Quarantined`, `Merged`. No whimsy, no puns, no exclamation in working UI.
- **Brand voice is rationed.** The trau personality (prairie / buffalo / soil) appears **only** in empty states
  and the wordmark — a welcome line on a blank screen, a "nothing on the range yet" placeholder. Everywhere a
  user is reading state or driving the agent, stay terse and literal.
- **Numbers and ids are mono and exact.** Show `$0.42`, `COD-738`, `3m 12s` — never rounded-away or dressed up.

### Output constraints for V0

- **One screen, dark-only.** No light theme, no theme toggle.
- **Static, self-contained.** No routing, no auth, no real data fetching. Use realistic inline placeholder data
  and render the empty / loading / error states the spec asks for.
- **Tokens only.** Every color resolves to a token above; every mono element uses Geist Mono; every prose element
  uses Space Grotesk.
- **Use the signature components** where the spec calls for them; don't reinvent card/pill/eyebrow patterns.
- **Respect `prefers-reduced-motion`.** Decorative motion (cursor blink, any animated texture) must have a
  reduced-motion fallback.

<!-- ===== END PREAMBLE ===== -->

---

## Generation workflow

The design pipeline for each screen:

1. **Assemble** the prompt: this preamble + the screen spec (from the paired HITL "V0 prompt + design" slice,
   e.g. COD-727…735).
2. **Generate** in V0. Iterate in V0 until the screen reads as trau — correct tokens, fonts, signature
   components, tone.
3. **Land** the approved design in **[trau-cli-web](https://github.com/RomkaLTU/trau-cli-web)** (the separate UI
   repo). This repo is the design gallery / reference implementation in V0's native Next.js idiom. Approval there
   is what unblocks the paired wiring slice.
4. **Adapt** in the wiring slice: port the design from trau-cli-web into the embedded SPA at `web/`, following the
   [adaptation notes](#nextjs--vite-adaptation-notes). The V0 output is a **reference, never a wholesale import** —
   markup and classes port over; framework idioms are rewritten to the pinned stack.

The embedded SPA (`web/`) is the shipping artifact — it builds into `internal/webserver/dist` and is served by
`trau serve`. trau-cli-web is scaffolding for the design phase only.

---

## Next.js → Vite adaptation notes

V0 emits Next.js (App Router, React Server Components, `next/*` packages). The embedded SPA is a **pinned** stack:
**Vite + TanStack Router + TanStack Query + Tailwind v4 (CSS-first) + vendored shadcn (new-york)**. When adapting a
V0 design into `web/`, translate each Next.js idiom:

- **`"use client"` / RSC** — delete the directive. The SPA is fully client-side (`rsc: false`); there are no
  server components or server actions.
- **Fonts** — V0 uses `next/font/google` (`Space_Grotesk`, `Geist_Mono`). Delete those imports. Fonts are
  npm-bundled (no CDN) and wired to `--font-sans` / `--font-mono` in `web/src/styles.css` (COD-723). Use the
  `font-sans` / `font-mono` utilities.
- **Routing & links** — `next/link` (`<Link href>`) and `next/navigation` (`useRouter`, `usePathname`) become
  TanStack Router: `<Link to>`, `useRouterState`, and active styling via `activeProps` / `activeOptions`. Each
  screen becomes a route module under `web/src/routes/*.tsx` exporting `createFileRoute(...)` (see the existing
  `__root.tsx` shell and route files for the idiom).
- **Metadata / `app/layout.tsx` head** — drop. The SPA `<head>` lives in `web/index.html`; the dark-mode class is
  set there by a small inline script.
- **Images** — `next/image` → plain `<img>` or inline SVG. Drop `fill` / `priority` / `sizes`.
- **Data fetching** — V0 hardcodes placeholder data. Replace with `@tanstack/react-query` `useQuery` against the
  existing fetchers in `web/src/lib/*.ts`. Keep V0's markup; swap the placeholder arrays for query data and wire
  the loading / empty / error states the design already shows.
- **shadcn components** — V0 pulls shadcn from its own registry. Use the **already-vendored** components in
  `web/src/components/ui/` (style `new-york`, `baseColor: neutral`). Import paths (`@/components/ui/*`,
  `@/lib/utils`) already match the SPA's aliases — don't re-run `npx shadcn add` blindly; reconcile against the
  vendored versions. Note the vendored components import Radix from the **unified `radix-ui` package**
  (`import { Slot } from "radix-ui"`), not per-primitive `@radix-ui/react-*` — rewrite any per-primitive imports
  V0 produces. Variants use `cva` + `cn()` (clsx + tailwind-merge); keep that pattern.
- **Tailwind config** — V0 may emit `tailwind.config.ts` and `@config`. The pinned stack is **Tailwind v4
  CSS-first**: no JS config file. Tokens live in `@theme inline` in `web/src/styles.css`. Move any theme
  extension into CSS `@theme`; convert hardcoded hex classes to token utilities (`text-teal`, `bg-card`,
  `border-border`).
- **Icons** — V0 uses `lucide-react`, which is already a dependency. Keep as-is.
- **Charts** — the pinned stack standardizes on **Recharts** (already a dependency). Port any V0 chart to
  Recharts and color series from the semantic palette.
- **Component boundaries** — V0 tends to emit one monolithic page component. Extract the shared signature pieces
  (terminal-window card, status pill, mono eyebrow) into reusable components under `web/src/components/` rather
  than duplicating them per screen.
- **Dark-only** — strip any light-theme variants or theme toggles V0 adds; the app is dark-only.

---

## Versioning

This kit is versioned so every screen prompt records which preamble it was generated against.

- Bump the **Version** in the header on any change to tokens, fonts, component recipes, palette mapping, tone, or
  output constraints, and add a changelog entry.
- When you run a screen prompt, note the preamble version alongside the design (in the trau-cli-web commit / the
  paired Linear slice) so a regenerated screen can be diffed against the version it was born from.

### Changelog

- **v1 (2026-07-05)** — initial kit: trau.sh token block, Space Grotesk + Geist Mono, signature component recipes
  (terminal-window card, status pill, mono eyebrows, block cursor, furrow-grid, texture layers), semantic
  run-state mapping, copy tone, generation workflow, and Next.js → Vite adaptation notes.
