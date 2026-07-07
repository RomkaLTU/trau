import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { useQuery } from '@tanstack/react-query'

import { loadStoredRepo, resolveActiveRepo, storeRepo } from '@/lib/active-repo'
import type { RepoView } from '@/lib/instances'
import { reposQueryOptions } from '@/lib/runs'

interface ActiveRepoValue {
  repo: string | null
  repos: RepoView[]
  setRepo: (name: string) => void
}

const ActiveRepoContext = createContext<ActiveRepoValue | null>(null)

export function ActiveRepoProvider({ children }: { children: ReactNode }) {
  const { data } = useQuery(reposQueryOptions)
  const repos = data?.repos ?? []

  const [stored, setStored] = useState<string | null>(() => loadStoredRepo())
  const repo = resolveActiveRepo(repos, stored)

  const setRepo = useCallback((name: string) => {
    setStored(name)
    storeRepo(name)
  }, [])

  const value = useMemo<ActiveRepoValue>(
    () => ({ repo, repos, setRepo }),
    [repo, repos, setRepo],
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
