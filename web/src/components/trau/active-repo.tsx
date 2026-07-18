import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'

import {
  ALL_SCOPE,
  autoScopeTarget,
  loadLastRepo,
  loadStoredScope,
  repoRouteAction,
  resolveScope,
  storeScope,
} from '@/lib/active-repo'
import type { RepoView } from '@/lib/instances'
import {
  loadRecents,
  projectRecent,
  recordRecent,
  saveRecents,
} from '@/lib/recents'
import { reposQueryOptions } from '@/lib/runs'

interface ActiveRepoValue {
  // scope is what the switcher shows as selected: a repo name or ALL_SCOPE.
  scope: string
  // repo is the concrete resolved repo, or null under "All projects" / no repos.
  repo: string | null
  // isAll is true under "All projects" — operate pages gate on it.
  isAll: boolean
  repos: RepoView[]
  setScope: (scope: string) => void
  setRepo: (name: string) => void
  // autoScope jumps out of "All projects" to a sensible repo (lone/last-used),
  // returning it, or null when the caller should open the switcher to choose.
  autoScope: () => string | null
  // openSwitcher pulses the repo switcher open so a gated click points at the fix.
  openSwitcher: () => void
  switcherSignal: number
}

const ActiveRepoContext = createContext<ActiveRepoValue | null>(null)

export function ActiveRepoProvider({ children }: { children: ReactNode }) {
  const { data } = useQuery(reposQueryOptions)
  const repos = data?.repos ?? []

  const [stored, setStored] = useState<string | null>(() => loadStoredScope())
  const { scope, repo, isAll } = resolveScope(repos, stored)

  const [switcherSignal, setSwitcherSignal] = useState(0)

  const setScope = useCallback((next: string) => {
    setStored(next)
    storeScope(next)
    if (next !== ALL_SCOPE) {
      saveRecents(recordRecent(loadRecents(), projectRecent(next, Date.now())))
    }
  }, [])

  const openSwitcher = useCallback(() => setSwitcherSignal((n) => n + 1), [])

  const autoScope = useCallback(() => {
    const target = autoScopeTarget(repos, loadLastRepo())
    if (target) setScope(target)
    return target
  }, [repos, setScope])

  const value = useMemo<ActiveRepoValue>(
    () => ({
      scope,
      repo,
      isAll,
      repos,
      setScope,
      setRepo: setScope,
      autoScope,
      openSwitcher,
      switcherSignal,
    }),
    [scope, repo, isAll, repos, setScope, autoScope, openSwitcher, switcherSignal],
  )

  return (
    <ActiveRepoContext.Provider value={value}>
      {children}
    </ActiveRepoContext.Provider>
  )
}

export function useActiveRepo(): ActiveRepoValue {
  const ctx = useContext(ActiveRepoContext)
  if (!ctx) {
    throw new Error('useActiveRepo must be used within an ActiveRepoProvider')
  }
  return ctx
}

// useRepoRouteScope binds a repo-bound route ($repo URL segment) to the active
// scope. It adopts the route's repo on entry, then leaves for the repo-neutral
// runs list once the user switches the scope to another project — so the switcher
// no longer silently desyncs from a live run that stays on screen.
export function useRepoRouteScope(routeRepo: string): void {
  const { repo: scopeRepo, setRepo } = useActiveRepo()
  const navigate = useNavigate()
  const synced = useRef(false)

  useEffect(() => {
    synced.current = false
  }, [routeRepo])

  useEffect(() => {
    switch (repoRouteAction(routeRepo, scopeRepo, synced.current)) {
      case 'stay':
        synced.current = true
        break
      case 'adopt':
        setRepo(routeRepo)
        break
      case 'leave':
        void navigate({ to: '/runs' })
        break
    }
  }, [routeRepo, scopeRepo, setRepo, navigate])
}

export { ALL_SCOPE }
