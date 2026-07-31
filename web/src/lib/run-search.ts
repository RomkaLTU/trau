import { matchesQuery } from './palette-filter'
import type { Run } from './runs'

const PALETTE_MATCH_LIMIT = 5

export function matchRuns(runs: readonly Run[], query: string): Run[] {
  if (query.trim() === '') return []
  return runs
    .filter((run) => matchesQuery(query, [run.ticket, run.title ?? '']))
    .sort((a, b) => (b.updated_at ?? '').localeCompare(a.updated_at ?? ''))
    .slice(0, PALETTE_MATCH_LIMIT)
}
