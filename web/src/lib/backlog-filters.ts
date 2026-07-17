import {
  createParser,
  type inferParserType,
  parseAsArrayOf,
  parseAsBoolean,
  parseAsInteger,
  parseAsString,
  parseAsStringLiteral,
} from 'nuqs'

import { STATE_GROUPS, type BacklogParams, type StateGroup } from './backlog'

export const SOURCE_VALUES = ['internal', 'synced'] as const

// The hub reads an empty state filter as "every group", so the planned-first
// default is sent explicitly to keep done and canceled off the board.
export const DEFAULT_STATE_GROUPS: readonly StateGroup[] = [
  'started',
  'unstarted',
  'backlog',
  'unknown',
]

export function effectiveStateGroups(state: string[]): string[] {
  return state.length > 0 ? state : [...DEFAULT_STATE_GROUPS]
}

// Reject non-positive pages so a malformed ?page=0/-1 falls back to 1 instead of
// deriving a negative offset.
const parseAsPage = createParser({
  parse(value) {
    const n = parseAsInteger.parse(value)
    return n !== null && n >= 1 ? n : null
  },
  serialize: parseAsInteger.serialize,
}).withDefault(1)

// state is array-typed so the planned-first slice can later default it to a group
// set; today's single-value select feeds it a one-element array.
export const backlogFilterParsers = {
  q: parseAsString.withDefault(''),
  state: parseAsArrayOf(parseAsString, ',').withDefault([]),
  label: parseAsString.withDefault(''),
  // assignee carries a resolved token the client never interprets: `me`,
  // `unassigned`, or an assignee id. The hub resolves Me per repo binding.
  assignee: parseAsString.withDefault(''),
  source: parseAsStringLiteral(SOURCE_VALUES),
  // archived swaps the board to the archived view; the default false keeps the
  // param out of the URL until it is toggled on.
  archived: parseAsBoolean.withDefault(false),
  page: parseAsPage,
}

export type BacklogFilters = inferParserType<typeof backlogFilterParsers>

export function backlogParamsFromFilters(
  filters: BacklogFilters,
  pageSize: number,
): BacklogParams {
  return {
    q: filters.q,
    label: filters.label,
    assignee: filters.assignee,
    state: effectiveStateGroups(filters.state).join(','),
    source: filters.source ?? '',
    archived: filters.archived,
    limit: pageSize,
    offset: (filters.page - 1) * pageSize,
  }
}

// toggleStateGroup adds or removes a group from the multi-select state filter,
// always returning the survivors in STATE_GROUPS order so the serialized `state`
// param — and the backlog query key derived from it — stays stable regardless of
// the order the user clicked.
export function toggleStateGroup(current: string[], group: string): string[] {
  const next = new Set(current)
  if (next.has(group)) {
    next.delete(group)
  } else {
    next.add(group)
  }
  return STATE_GROUPS.filter((g) => next.has(g))
}

export function hasActiveFilters(filters: BacklogFilters): boolean {
  return (
    filters.q !== '' ||
    filters.label !== '' ||
    filters.assignee !== '' ||
    filters.state.length > 0 ||
    filters.source !== null ||
    filters.archived
  )
}
