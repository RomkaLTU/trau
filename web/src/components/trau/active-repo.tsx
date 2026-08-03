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
  findRepo,
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
  // root is the resolved repo's root, the only thing that separates two repos
  // registered under the same name.
  root: string | null
  // isAll is true under "All projects" — operate pages gate on it.
  isAll: boolean
  repos: RepoView[]
  // setScope takes a repo ident — a root, which addresses one of two same-named
  // repos, or a name — or ALL_SCOPE.
  setScope: (ident: string) => void
  setRepo: (ident: string) => void
  // autoScope jumps out of "All projects" to a sensible repo (lone/last-used),
  // returning it, or null when the caller should open the switcher to choose.
  autoScope: () => RepoView | null
  // openSwitcher opens the repo picker so a gated click points at the fix.
  openSwitcher: () => void
  switcherSignal: number
}

const ActiveRepoContext = createContext<ActiveRepoValue | null>(null)

export function ActiveRepoProvider({ children }: { children: ReactNode }) {
  const { data } = useQuery(reposQueryOptions)
  const repos = useMemo(() => data?.repos ?? [], [data])

  const [stored, setStored] = useState<string | null>(() => loadStoredScope())
  const { scope, repo, root, isAll } = resolveScope(repos, stored)

  const [switcherSignal, setSwitcherSignal] = useState(0)

  // The root is what gets persisted, so a reload lands on the repo that was
  // picked rather than on whichever one happens to carry the name first.
  const setScope = useCallback(
    (ident: string) => {
      const picked = findRepo(repos, ident)
      const next = picked?.root ?? ident
      setStored(next)
      storeScope(next)
      if (picked) {
        saveRecents(
          recordRecent(loadRecents(), projectRecent(picked, Date.now())),
        )
      }
    },
    [repos],
  )

  const openSwitcher = useCallback(() => setSwitcherSignal((n) => n + 1), [])

  const autoScope = useCallback(() => {
    const target = autoScopeTarget(repos, loadLastRepo())
    if (target) setScope(target.root)
    return target
  }, [repos, setScope])

  const value = useMemo<ActiveRepoValue>(
    () => ({
      scope,
      repo,
      root,
      isAll,
      repos,
      setScope,
      setRepo: setScope,
      autoScope,
      openSwitcher,
      switcherSignal,
    }),
    [
      scope,
      repo,
      root,
      isAll,
      repos,
      setScope,
      autoScope,
      openSwitcher,
      switcherSignal,
    ],
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
// scope. It adopts the route's repo on entry when a single project is scoped, then
// leaves for the repo-neutral runs list once the user switches the scope to another
// project — so the switcher no longer silently desyncs from a live run on screen.
export function useRepoRouteScope(routeRepo: string): void {
  const { repo: scopeRepo, isAll, setRepo } = useActiveRepo()
  const navigate = useNavigate()
  const synced = useRef(false)

  useEffect(() => {
    synced.current = false
  }, [routeRepo])

  useEffect(() => {
    switch (repoRouteAction(routeRepo, scopeRepo, isAll, synced.current)) {
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
  }, [routeRepo, scopeRepo, isAll, setRepo, navigate])
}

export { ALL_SCOPE }
