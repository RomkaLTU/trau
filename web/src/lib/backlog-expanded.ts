type Store = Pick<Storage, 'getItem' | 'setItem'>

// KEY_PREFIX namespaces the expanded-epic set per repo. Expansion is view state,
// not semantic state, so it lives in localStorage rather than the URL.
const KEY_PREFIX = 'trau.backlog.expanded.'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadExpandedEpics(repo: string): Set<string> {
  if (!repo) return new Set()
  const raw = browserStore()?.getItem(KEY_PREFIX + repo)
  if (!raw) return new Set()
  try {
    const ids = JSON.parse(raw)
    return Array.isArray(ids) ? new Set(ids) : new Set()
  } catch {
    return new Set()
  }
}

export function storeExpandedEpics(repo: string, ids: Set<string>): void {
  if (!repo) return
  browserStore()?.setItem(KEY_PREFIX + repo, JSON.stringify([...ids]))
}
