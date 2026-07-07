import type { RepoView } from './instances'

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const ACTIVE_REPO_KEY = 'trau.active-repo'

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadStoredRepo(): string | null {
  return browserStore()?.getItem(ACTIVE_REPO_KEY) ?? null
}

export function storeRepo(name: string | null): void {
  const store = browserStore()
  if (!store) return
  if (name) store.setItem(ACTIVE_REPO_KEY, name)
  else store.removeItem(ACTIVE_REPO_KEY)
}

// resolveActiveRepo keeps the checked-out repo when it still exists, and
// otherwise falls back to a live repo, then the first repo, then nothing —
// so a repo that vanishes (unregistered/unknown) never leaves the shell stuck.
export function resolveActiveRepo(
  repos: readonly RepoView[],
  stored: string | null,
): string | null {
  if (stored && repos.some((r) => r.name === stored)) return stored
  return repos.find((r) => r.live)?.name ?? repos[0]?.name ?? null
}
