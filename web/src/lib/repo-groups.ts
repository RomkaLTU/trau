type Store = Pick<Storage, 'getItem' | 'setItem'>

// Whether a folder group is folded away is view state, not semantic state, so it
// lives in localStorage rather than on the project.
const GROUPS_KEY = 'trau.project-groups'

// GroupState carries only the groups the user has folded or unfolded by hand. An
// absent entry follows the default rule instead, so a deliberate choice survives
// a scope change while every untouched group keeps tracking the active repo.
export type GroupState = Record<string, 'open' | 'closed'>

function browserStore(): Store | null {
  try {
    return globalThis.localStorage ?? null
  } catch {
    return null
  }
}

export function loadGroupState(): GroupState {
  const raw = browserStore()?.getItem(GROUPS_KEY)
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return {}
    if (Array.isArray(parsed)) return {}
    return Object.fromEntries(
      Object.entries(parsed).filter(
        ([, fold]) => fold === 'open' || fold === 'closed',
      ),
    )
  } catch {
    return {}
  }
}

export function saveGroupState(state: GroupState): void {
  browserStore()?.setItem(GROUPS_KEY, JSON.stringify(state))
}

// isGroupOpen answers for one group: an explicit fold wins, otherwise the group
// holding the scoped repo is the one that opens — under "All repos" none does.
export function isGroupOpen(
  state: GroupState,
  project: string,
  memberNames: readonly string[],
  activeRepo: string | null,
): boolean {
  const fold = state[project]
  if (fold) return fold === 'open'
  return activeRepo !== null && memberNames.includes(activeRepo)
}
