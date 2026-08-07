import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import { type StateGroup } from './backlog'

export interface StatusColumn {
  name: string
  suggestedGroup: string
}

export interface StatusPinOption {
  name: string
  category: string
}

export interface StatusOptions {
  provider: string
  grouping: StatusColumn[]
  pinOptions: StatusPinOption[]
  error?: string
  hint?: string
}

// A provider with no status-mapping editor answers 404, which is an answer and
// not a failure: null means "render the generic rows", while a thrown error is a
// hub the settings page could not reach at all.
async function fetchStatusOptions(repo: string): Promise<StatusOptions | null> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/tracker/status-options`,
  )
  if (res.status === 404) return null
  if (!res.ok) throw new Error(`status options request failed: ${res.status}`)
  return res.json()
}

export const statusOptionsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['status-options', repo],
    queryFn: () => fetchStatusOptions(repo),
    enabled: repo !== '',
    staleTime: 60_000,
  })

export const BOARD_STATES_KEY = 'AZURE_BOARD_STATES'

export const PIN_KEYS = [
  'STATUS_TODO',
  'STATUS_IN_PROGRESS',
  'STATUS_IN_REVIEW',
  'STATUS_DONE',
] as const

export type PinKey = (typeof PIN_KEYS)[number]

// The loop's own stage names, so a pin row reads as the lifecycle step it pins
// rather than as the key it writes.
export const PIN_LABELS: Record<PinKey, string> = {
  STATUS_TODO: 'To Do',
  STATUS_IN_PROGRESS: 'In Progress',
  STATUS_IN_REVIEW: 'In Review',
  STATUS_DONE: 'Done',
}

export const STATUS_MAP_KEYS: string[] = [BOARD_STATES_KEY, ...PIN_KEYS]

export const MAPPABLE_GROUPS = [
  'backlog',
  'unstarted',
  'started',
  'done',
  'canceled',
] as const

export type MappableGroup = (typeof MAPPABLE_GROUPS)[number]

// UNMAPPED is the row state that writes nothing. A set mapping is exhaustive, so
// leaving a column out is a real choice with a visible consequence — its work
// groups as Other — and the select says so rather than hiding the row.
export const UNMAPPED = 'unknown'

export interface BoardStatePair {
  name: string
  group: StateGroup
}

function isMappable(group: string): group is MappableGroup {
  return (MAPPABLE_GROUPS as readonly string[]).includes(group)
}

// toGroup narrows a raw string — a select's value, a suggestion the hub sent —
// to the closed set a row may carry; anything else is a row that maps nowhere.
export function toGroup(value: string): StateGroup {
  return isMappable(value) ? value : UNMAPPED
}

// parseBoardStates reads the AZURE_BOARD_STATES spec the same way the hub does:
// comma-separated "<column>=<group>" pairs split on the first =, with a pair
// naming a group trau does not have dropped rather than failing the whole value.
export function parseBoardStates(value: string): BoardStatePair[] {
  const out: BoardStatePair[] = []
  for (const pair of value.split(',')) {
    if (pair.trim() === '') continue
    const at = pair.indexOf('=')
    if (at < 0) continue
    const name = pair.slice(0, at).trim()
    const group = pair.slice(at + 1).trim().toLowerCase()
    if (name === '' || !isMappable(group)) continue
    out.push({ name, group })
  }
  return out
}

export function serializeBoardStates(rows: readonly BoardStatePair[]): string {
  return rows
    .filter((row) => isMappable(row.group))
    .map((row) => `${row.name.trim()}=${row.group}`)
    .join(',')
}

// boardNameError rejects a column name the grammar cannot express: , separates
// pairs and = separates a name from its group, so neither can appear in a name.
export function boardNameError(name: string): string | null {
  const trimmed = name.trim()
  if (trimmed === '') return 'Enter a column name.'
  if (trimmed.includes(',') || trimmed.includes('=')) {
    return 'A column name cannot contain , or = — the mapping separates pairs on , and names from groups on =.'
  }
  return null
}

export interface GroupingRow extends BoardStatePair {
  suggested: StateGroup
  // onBoard is false for a row that exists only because the repo's mapping names
  // it — a column since renamed, or every row when the board could not be read.
  onBoard: boolean
}

// deriveGroupingRows builds the editor's rows from the board's columns and the
// mapping the repo has today. With no mapping written the selects prefill from
// the suggestions and nothing has been written yet; with one written it is
// authoritative, so a column it omits shows as unmapped rather than as its
// suggestion. A name the mapping carries but the board does not is kept, so
// saving never silently drops it.
export function deriveGroupingRows(
  columns: StatusColumn[],
  current: string,
): GroupingRow[] {
  const mapped = parseBoardStates(current)
  const byName = new Map(mapped.map((p) => [p.name.toLowerCase(), p.group]))
  const rows: GroupingRow[] = []
  const seen = new Set<string>()

  for (const col of columns) {
    const key = col.name.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    const suggested = toGroup(col.suggestedGroup)
    rows.push({
      name: col.name,
      suggested,
      group: mapped.length === 0 ? suggested : (byName.get(key) ?? UNMAPPED),
      onBoard: true,
    })
  }
  for (const pair of mapped) {
    const key = pair.name.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    rows.push({
      name: pair.name,
      group: pair.group,
      suggested: UNMAPPED,
      onBoard: false,
    })
  }
  return rows
}
