import type { ThemeSummary } from './palette'
import type { ResolvedTheme } from './theme'

// CORE_ROLES are the twelve semantic roles every theme mode defines, in the
// order internal/theme lists them: the two accents, the four status colors, the
// four text tones, then the surfaces the rest sit on.
export const CORE_ROLES = [
  'brand',
  'accent',
  'success',
  'error',
  'warning',
  'info',
  'text',
  'subtle',
  'faint',
  'surface',
  'border',
  'ink',
] as const

export type CoreRole = (typeof CORE_ROLES)[number]

export interface ThemeModeDocument {
  roles: Record<string, string>
  vars?: Record<string, string>
  tui?: Record<string, string>
}

// ThemeDocument is the theme file: the shape the hub validates, the editor
// exports, and a future trau.sh submission carries.
export interface ThemeDocument {
  name: string
  slug: string
  author?: string
  version?: string
  modes: Partial<Record<ResolvedTheme, ThemeModeDocument>>
}

// DraftMode holds what the inputs show. roles is raw text, so a half-typed hex
// stays on screen; initial keeps the last colors the mode was known good at, and
// is what the preview substitutes while a role is mid-edit. vars and tui ride
// along untouched — v1 has no editing surface for them, but a theme duplicated
// from one that pins them must not lose them.
export interface DraftMode {
  roles: Record<string, string>
  initial: Record<string, string>
  vars?: Record<string, string>
  tui?: Record<string, string>
}

export interface ThemeDraft {
  name: string
  author?: string
  version?: string
  source: string
  modes: Partial<Record<ResolvedTheme, DraftMode>>
}

export const EDITOR_MODES: readonly ResolvedTheme[] = ['light', 'dark']

// The theme format's own color syntax (internal/theme.ParseColor): a hex literal
// and nothing else, because a resolved value reaches the browser as raw CSS.
const THEME_COLOR = /^#(?:[0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i

export function isThemeColor(value: string): boolean {
  return THEME_COLOR.test(value.trim())
}

// slugify derives the file name a theme saves under from its display name. It
// produces the format's slug syntax or an empty string, which the editor reports
// as "name this theme" rather than saving something unaddressable.
export function slugify(name: string): string {
  const slug = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return /^[a-z0-9][a-z0-9-]*$/.test(slug) ? slug : ''
}

// asThemeDocument narrows what the themes resource handed back. The hub only
// serves documents it validated, so this guards against an unreachable or
// mismatched build rather than against bad colors.
export function asThemeDocument(input: unknown): ThemeDocument | null {
  if (typeof input !== 'object' || input === null) return null
  const doc = input as Partial<ThemeDocument>
  if (typeof doc.slug !== 'string' || typeof doc.name !== 'string') return null
  if (typeof doc.modes !== 'object' || doc.modes === null) return null
  for (const mode of EDITOR_MODES) {
    const value = doc.modes[mode]
    if (value === undefined) continue
    if (typeof value !== 'object' || value === null) return null
    if (typeof value.roles !== 'object' || value.roles === null) return null
  }
  return doc as ThemeDocument
}

export function draftModes(draft: ThemeDraft): ResolvedTheme[] {
  return EDITOR_MODES.filter((mode) => draft.modes[mode] !== undefined)
}

function modeFromDocument(mode: ThemeModeDocument): DraftMode {
  const roles: Record<string, string> = {}
  for (const role of CORE_ROLES) roles[role] = mode.roles[role] ?? ''
  return { roles, initial: { ...roles }, vars: mode.vars, tui: mode.tui }
}

// draftFromDocument opens a theme for editing. name overrides the source's own,
// which is how 'Duplicate & edit' lands on a new slug instead of silently
// offering to overwrite the theme it copied.
export function draftFromDocument(
  doc: ThemeDocument,
  name = doc.name,
): ThemeDraft {
  const modes: Partial<Record<ResolvedTheme, DraftMode>> = {}
  for (const mode of EDITOR_MODES) {
    const source = doc.modes[mode]
    if (source) modes[mode] = modeFromDocument(source)
  }
  return {
    name,
    author: doc.author,
    version: doc.version,
    source: doc.slug,
    modes,
  }
}

export function setRole(
  draft: ThemeDraft,
  mode: ResolvedTheme,
  role: string,
  value: string,
): ThemeDraft {
  const current = draft.modes[mode]
  if (!current) return draft
  const roles = { ...current.roles, [role]: value }
  // A value that parses becomes the preview's new floor, so backspacing over a
  // hex never repaints the app with a color the user already replaced.
  const initial = isThemeColor(value)
    ? { ...current.initial, [role]: value.trim() }
    : current.initial
  return { ...draft, modes: { ...draft.modes, [mode]: { ...current, roles, initial } } }
}

// addMode seeds a mode the draft does not paint yet. seed is the same polarity
// of some installed theme, so a new dark mode starts from dark colors rather
// than from the light ones the user just wrote.
export function addMode(
  draft: ThemeDraft,
  mode: ResolvedTheme,
  seed: Record<string, string>,
): ThemeDraft {
  if (draft.modes[mode]) return draft
  const roles: Record<string, string> = {}
  for (const role of CORE_ROLES) roles[role] = seed[role] ?? '#000000'
  return {
    ...draft,
    modes: { ...draft.modes, [mode]: { roles, initial: { ...roles } } },
  }
}

// removeMode drops a polarity. The format needs at least one, and a theme with
// no modes paints nothing, so the last one stays.
export function removeMode(draft: ThemeDraft, mode: ResolvedTheme): ThemeDraft {
  if (draftModes(draft).length < 2) return draft
  const modes = { ...draft.modes }
  delete modes[mode]
  return { ...draft, modes }
}

export interface DraftIssues {
  name: string | null
  slug: string | null
  roles: Partial<Record<ResolvedTheme, Record<string, string>>>
}

export function hasIssues(issues: DraftIssues): boolean {
  if (issues.name !== null || issues.slug !== null) return true
  return EDITOR_MODES.some(
    (mode) => Object.keys(issues.roles[mode] ?? {}).length > 0,
  )
}

// draftIssues reports every field the hub would refuse, before the request goes
// out. reserved are the predefined slugs, which a saved theme may never claim.
export function draftIssues(
  draft: ThemeDraft,
  reserved: readonly string[],
): DraftIssues {
  const slug = slugify(draft.name)
  const issues: DraftIssues = { name: null, slug: null, roles: {} }
  if (draft.name.trim() === '') {
    issues.name = 'name this theme'
  } else if (slug === '') {
    issues.name = 'the name needs a letter or a digit'
  }
  if (slug !== '' && reserved.includes(slug)) {
    issues.slug = `${slug} is a theme that ships with trau — pick another name`
  }
  for (const mode of draftModes(draft)) {
    const bad: Record<string, string> = {}
    const roles = draft.modes[mode]!.roles
    for (const role of CORE_ROLES) {
      const value = roles[role] ?? ''
      if (!isThemeColor(value)) bad[role] = 'expected a hex color like #7d56f4'
    }
    if (Object.keys(bad).length > 0) issues.roles[mode] = bad
  }
  return issues
}

function documentMode(
  mode: DraftMode,
  safe: boolean,
  dropPins: boolean,
): ThemeModeDocument {
  const roles: Record<string, string> = {}
  for (const role of CORE_ROLES) {
    const raw = (mode.roles[role] ?? '').trim()
    roles[role] = !safe || isThemeColor(raw) ? raw : (mode.initial[role] ?? '')
  }
  const out: ThemeModeDocument = { roles }
  if (dropPins) return out
  if (mode.vars) out.vars = mode.vars
  if (mode.tui) out.tui = mode.tui
  return out
}

function buildDocument(
  draft: ThemeDraft,
  slug: string,
  safe: boolean,
  dropPins: boolean,
): ThemeDocument {
  const doc: ThemeDocument = { name: draft.name.trim() || slug, slug, modes: {} }
  if (draft.author) doc.author = draft.author
  if (draft.version) doc.version = draft.version
  for (const mode of draftModes(draft)) {
    doc.modes[mode] = documentMode(draft.modes[mode]!, safe, dropPins)
  }
  return doc
}

// previewDocument is the draft as something the resolver will accept right now:
// a role mid-edit falls back to the last color it parsed at, and an unnamed
// draft borrows a placeholder slug. Invalid input then shows inline without the
// live preview going dark.
export function previewDocument(draft: ThemeDraft, dropPins = false): ThemeDocument {
  return buildDocument(draft, slugify(draft.name) || 'draft', true, dropPins)
}

// exportDocument is the theme file itself — what a save installs and what the
// download and clipboard copies carry. Callers check draftIssues first.
export function exportDocument(draft: ThemeDraft, dropPins = false): ThemeDocument {
  return buildDocument(draft, slugify(draft.name), false, dropPins)
}

// pinCounts totals the vars and tui entries a duplicated theme brought along.
// They win over everything the editor writes, so the editor has to say they are
// there rather than let an edit look ignored.
export function pinCounts(draft: ThemeDraft): { vars: number; tui: number } {
  let vars = 0
  let tui = 0
  for (const mode of draftModes(draft)) {
    vars += Object.keys(draft.modes[mode]!.vars ?? {}).length
    tui += Object.keys(draft.modes[mode]!.tui ?? {}).length
  }
  return { vars, tui }
}

// themeJSON renders the document in the shape the hub writes to disk: two-space
// indent, trailing newline. Key order follows the draft rather than Go's sorted
// map keys, so a downloaded file is the same theme as the saved one, not the
// same bytes.
export function themeJSON(doc: ThemeDocument): string {
  return `${JSON.stringify(doc, null, 2)}\n`
}

export function downloadName(doc: ThemeDocument): string {
  return `${doc.slug || 'theme'}.json`
}

// seedRoles picks the colors a newly added mode starts from: that polarity of
// the theme the draft came from when it paints it, else of any installed theme
// that does, else the mode the draft already has.
export function seedRoles(
  draft: ThemeDraft,
  mode: ResolvedTheme,
  themes: readonly ThemeSummary[],
): Record<string, string> {
  const fromSource = themes.find((t) => t.slug === draft.source)?.roles?.[mode]
  if (fromSource) return fromSource
  const other = draftModes(draft)[0]
  if (other) return draft.modes[other]!.initial
  return themes.find((t) => t.roles?.[mode])?.roles?.[mode] ?? {}
}
