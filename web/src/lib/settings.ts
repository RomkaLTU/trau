import type { ConfigKey } from './config'

export const OTHER_SECTION = 'Other'

export const SECTION_ORDER = [
  'Tracker & issues',
  'Git & merge',
  'CI',
  'Providers & models',
  'Per-phase routing',
  'Pipeline behavior',
  'Verification',
  'Cost caps',
  'Grilling & triage',
  'Skills',
  'Agent runtime',
  'Hub & web server',
  'Retention',
  'TUI & notifications',
  'Time logging',
  'Paths & misc',
] as const

const SECTION_DESCRIPTIONS: Record<string, string> = {
  'Tracker & issues': 'Where tickets come from and how they are labeled.',
  'Git & merge': 'Branching, pushing, and merge automation.',
  CI: 'Which checks gate a merge and how long to wait for them.',
  'Providers & models': 'Default agent provider, models, and CLI wiring.',
  'Per-phase routing':
    'Override model, effort, and tool restrictions per pipeline phase.',
  'Pipeline behavior': 'Iteration limits and optional pipeline phases.',
  Verification: 'How finished work is checked before handoff.',
  'Cost caps': 'Hard spend limits per ticket and per day.',
  'Grilling & triage': 'Pre-run readiness checks on incoming tickets.',
  Skills: 'Skills required for runs and how they are installed.',
  'Agent runtime':
    'Timeouts, retries, and terminal geometry for agent processes.',
  'Hub & web server': 'Bind address, auth, and sync cadence for the web hub.',
  Retention: 'How long transcripts, events, and token records are kept.',
  'TUI & notifications': 'Terminal UI, theme, and desktop notifications.',
  'Time logging': 'Billable time capture and export.',
  'Paths & misc': 'Filesystem locations trau reads and writes.',
  [OTHER_SECTION]: 'Keys the catalog did not place in a known section.',
}

const HUB_RESTART_SECTIONS = new Set(['Hub & web server', 'Retention'])

export interface Section {
  id: string
  group: string
  description: string
  keys: ConfigKey[]
  primaryKeys: ConfigKey[]
  advancedKeys: ConfigKey[]
  modified: boolean
  hubRestart: boolean
}

export function sectionSlug(group: string): string {
  const slug = group
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return slug === '' ? 'section' : slug
}

export function sectionDescription(group: string): string {
  return SECTION_DESCRIPTIONS[group] ?? ''
}

export function appliesOnHubRestart(group: string): boolean {
  return HUB_RESTART_SECTIONS.has(group)
}

export function isModified(item: ConfigKey): boolean {
  if (item.secret) return Boolean(item.set) && item.layer !== 'default'
  return item.layer !== 'default'
}

const DASH = '—'
const DOTS = '••••••••'

export function displayValue(item: ConfigKey): string {
  if (item.secret) return item.set ? DOTS : DASH
  if (item.bool) return item.value === '1' ? 'on' : 'off'
  return item.value === '' ? DASH : item.value
}

export function matchesQuery(item: ConfigKey, query: string): boolean {
  if (query === '') return true
  const q = query.toLowerCase()
  return (
    item.key.toLowerCase().includes(q) ||
    (item.description ?? '').toLowerCase().includes(q)
  )
}

export const ROUTING_SECTION = 'Per-phase routing'
export const THEME_SECTION = 'TUI & notifications'

export const ROUTING_COLUMNS = ['MODEL', 'EFFORT', 'DISALLOWED_TOOLS'] as const
export type RoutingColumn = (typeof ROUTING_COLUMNS)[number]

export const COLUMN_LABELS: Record<RoutingColumn, string> = {
  MODEL: 'model',
  EFFORT: 'effort',
  DISALLOWED_TOOLS: 'disallowed tools',
}

const COLUMN_MATCH_ORDER: RoutingColumn[] = [
  'DISALLOWED_TOOLS',
  'MODEL',
  'EFFORT',
]

function parseRoutingKey(
  key: string,
): { provider: string; phase: string; column: RoutingColumn } | null {
  for (const column of COLUMN_MATCH_ORDER) {
    const suffix = `_${column}`
    if (!key.endsWith(suffix)) continue
    const head = key.slice(0, -suffix.length)
    const sep = head.indexOf('_')
    if (sep <= 0) return null
    return { provider: head.slice(0, sep), phase: head.slice(sep + 1), column }
  }
  return null
}

export interface PhaseMatrixModel {
  providers: string[]
  phases: Record<string, string[]>
  columns: Record<string, RoutingColumn[]>
}

export function derivePhaseMatrix(keys: ConfigKey[]): PhaseMatrixModel {
  const providers: string[] = []
  const phases: Record<string, string[]> = {}
  const columnSets: Record<string, Set<RoutingColumn>> = {}

  for (const item of keys) {
    const parts = parseRoutingKey(item.key)
    if (!parts) continue
    const seen = phases[parts.provider]
    if (seen === undefined) {
      providers.push(parts.provider)
      phases[parts.provider] = [parts.phase]
      columnSets[parts.provider] = new Set([parts.column])
      continue
    }
    if (!seen.includes(parts.phase)) seen.push(parts.phase)
    columnSets[parts.provider].add(parts.column)
  }

  const columns: Record<string, RoutingColumn[]> = {}
  for (const provider of providers) {
    columns[provider] = ROUTING_COLUMNS.filter((c) =>
      columnSets[provider].has(c),
    )
  }
  return { providers, phases, columns }
}

export function routingCellKey(
  provider: string,
  phase: string,
  column: string,
): string {
  return `${provider}_${phase}_${column}`
}

export function themeRoleLabel(key: string): string {
  return key.replace(/^THEME_/, '').toLowerCase()
}

const HEX_COLOR = /^#[0-9a-fA-F]{6}$/

export function isHexColor(value: string): boolean {
  return HEX_COLOR.test(value)
}

export function deriveSections(keys: ConfigKey[]): Section[] {
  const buckets = new Map<string, ConfigKey[]>()
  for (const item of keys) {
    const group =
      item.group && SECTION_DESCRIPTIONS[item.group] !== undefined
        ? item.group
        : OTHER_SECTION
    const bucket = buckets.get(group)
    if (bucket) {
      bucket.push(item)
    } else {
      buckets.set(group, [item])
    }
  }

  const ordered = [...SECTION_ORDER, OTHER_SECTION]
  const sections: Section[] = []
  for (const group of ordered) {
    const items = buckets.get(group)
    if (!items) continue
    sections.push({
      id: sectionSlug(group),
      group,
      description: sectionDescription(group),
      keys: items,
      primaryKeys: items.filter((k) => !k.advanced),
      advancedKeys: items.filter((k) => k.advanced),
      modified: items.some(isModified),
      hubRestart: appliesOnHubRestart(group),
    })
  }
  return sections
}
