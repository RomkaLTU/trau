import { NAV_GROUPS, type NavItem } from '@/components/trau/nav-items'

import type { Instance } from './instances'
import { isActiveState, toSessionState } from './overview'

export const RUN_SUGGESTION_CAP = 4

// A 'live' entry jumps into a run's live view, a 'run' entry into its detail;
// page entries carry the nav item so the palette reuses its icon and gating.
export type SuggestionEntry =
  | { kind: 'live' | 'run'; key: string; label: string; path: string }
  | { kind: 'page'; key: string; item: NavItem }

const NAV_ITEMS = NAV_GROUPS.flatMap((group) => group.items)

const RUN_PATH = /^\/(runs|live)\/([^/]+)\/([^/]+)$/

const RELATED_PAGES: Record<string, string[]> = {
  '/': ['/backlog', '/runs'],
  '/backlog': ['/inbox', '/loop', '/runs'],
  '/inbox': ['/backlog'],
  '/loop': ['/backlog', '/runs'],
  '/runs': ['/costs'],
}

function runEntry(kind: 'live' | 'run', repo: string, ticket: string): SuggestionEntry {
  const view = kind === 'live' ? 'live' : 'runs'
  return {
    kind,
    key: `${kind}:${repo}/${ticket}`,
    label: `${repo} · ${ticket}`,
    path: `/${view}/${encodeURIComponent(repo)}/${encodeURIComponent(ticket)}`,
  }
}

// Gated pages are dropped under "All repos" (scope null) so no suggestion
// dead-ends on the project gate.
function pageEntry(to: string, scope: string | null): SuggestionEntry | null {
  const item = NAV_ITEMS.find((i) => i.to === to)
  if (!item) return null
  if (scope === null && item.requiresProject) return null
  return { kind: 'page', key: `page:${to}`, item }
}

// suggestFor builds the palette's empty-query "Suggested" group: live jumps for
// active runs first (the scope's repo leading), then the pages a user is likely
// to want next from the current route. A route with no mapping and no active
// runs yields nothing, and the group is omitted.
export function suggestFor({
  pathname,
  scope,
  instances,
}: {
  pathname: string
  scope: string | null
  instances: readonly Instance[]
}): SuggestionEntry[] {
  const detail = RUN_PATH.exec(pathname)
  const view = detail?.[1]
  const repo = detail ? decodeURIComponent(detail[2]) : null
  const ticket = detail ? decodeURIComponent(detail[3]) : null

  const active = instances.filter(
    (i): i is Instance & { ticket: string } =>
      !!i.ticket && isActiveState(toSessionState(i.session_state)),
  )

  const entries: SuggestionEntry[] = []
  const jumps = active.filter((i) => !(i.repo === repo && i.ticket === ticket))
  const inScope = jumps.filter((i) => i.repo === scope)
  const rest = jumps.filter((i) => i.repo !== scope)
  for (const inst of [...inScope, ...rest].slice(0, RUN_SUGGESTION_CAP)) {
    entries.push(runEntry('live', inst.repo, inst.ticket))
  }

  if (view === 'runs' && repo && ticket) {
    if (active.some((i) => i.repo === repo && i.ticket === ticket)) {
      entries.push(runEntry('live', repo, ticket))
    }
    const runs = pageEntry('/runs', scope)
    if (runs) entries.push(runs)
  } else if (view === 'live' && repo && ticket) {
    entries.push(runEntry('run', repo, ticket))
  } else {
    for (const to of RELATED_PAGES[pathname] ?? []) {
      const entry = pageEntry(to, scope)
      if (entry) entries.push(entry)
    }
  }
  return entries
}
