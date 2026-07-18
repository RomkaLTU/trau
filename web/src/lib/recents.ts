import { NAV_GROUPS } from '@/components/trau/nav-items'

type Store = Pick<Storage, 'getItem' | 'setItem'>

const RECENTS_KEY = 'trau.web.recents'

export const RECENTS_CAP = 20

// A project entry carries no path — selecting it re-applies the scope instead
// of navigating — so the two shapes stay distinct rather than faking a path.
export type RecentEntry =
  | { kind: 'project'; key: string; label: string; sublabel?: string; at: number }
  | {
      kind: 'page' | 'run'
      key: string
      label: string
      sublabel?: string
      path: string
      at: number
    }

export function projectRecent(name: string, at: number): RecentEntry {
  return { kind: 'project', key: `project:${name}`, label: name, at }
}

const RUN_PATH = /^\/(runs|live)\/([^/]+)\/([^/]+)$/
const NAV_ITEMS = NAV_GROUPS.flatMap((group) => group.items)

// visitRecent maps a visited pathname to its history entry: nav destinations
// become page entries, run/live details become run entries, anything else is
// not history worth keeping.
export function visitRecent(pathname: string, at: number): RecentEntry | null {
  const detail = RUN_PATH.exec(pathname)
  if (detail) {
    const repo = decodeURIComponent(detail[2])
    const ticket = decodeURIComponent(detail[3])
    return {
      kind: 'run',
      key: `run:${repo}/${ticket}`,
      label: ticket,
      sublabel: repo,
      path: pathname,
      at,
    }
  }
  const item = NAV_ITEMS.find((i) => i.to === pathname)
  if (!item) return null
  return { kind: 'page', key: `page:${pathname}`, label: item.label, path: pathname, at }
}

export function recordRecent(
  list: readonly RecentEntry[],
  entry: RecentEntry,
): RecentEntry[] {
  return [entry, ...list.filter((e) => e.key !== entry.key)].slice(0, RECENTS_CAP)
}

// visibleRecents is what the palette shows on an empty query: newest first,
// minus where the user already is (current page, active scope) and entries
// whose repo has since been unregistered.
export function visibleRecents(
  list: readonly RecentEntry[],
  current: { path: string; repo: string | null; repos: readonly string[] },
  limit = 6,
): RecentEntry[] {
  const known = new Set(current.repos)
  return list
    .filter((e) => {
      if (e.kind === 'project') {
        return e.label !== current.repo && known.has(e.label)
      }
      if (e.path === current.path) return false
      return e.kind === 'page' || known.has(e.sublabel ?? '')
    })
    .slice(0, limit)
}

function isRecentEntry(e: unknown): e is RecentEntry {
  if (typeof e !== 'object' || e === null) return false
  const r = e as Record<string, unknown>
  return (
    typeof r.key === 'string' &&
    typeof r.label === 'string' &&
    typeof r.at === 'number' &&
    (r.kind === 'project' ||
      ((r.kind === 'page' || r.kind === 'run') && typeof r.path === 'string'))
  )
}

export function parseRecents(raw: string | null): RecentEntry[] {
  if (!raw) return []
  try {
    const parsed: unknown = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter(isRecentEntry) : []
  } catch {
    return []
  }
}

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadRecents(): RecentEntry[] {
  return parseRecents(browserStore()?.getItem(RECENTS_KEY) ?? null)
}

export function saveRecents(list: readonly RecentEntry[]): void {
  browserStore()?.setItem(RECENTS_KEY, JSON.stringify(list))
}
