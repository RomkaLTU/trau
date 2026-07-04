import { queryOptions } from '@tanstack/react-query'

export interface Instance {
  pid: number
  repo: string
  repo_root: string
  runs_dir: string
  started_at: string
  ticket?: string
  phase?: string
  phase_since?: string
}

export interface RepoView {
  name: string
  root: string
  runs_dir: string
  live: boolean
}

export interface InstancesResponse {
  instances: Instance[]
  repos: RepoView[]
}

async function fetchInstances(): Promise<InstancesResponse> {
  const res = await fetch('/api/v1/instances')
  if (!res.ok) {
    throw new Error(`instances request failed: ${res.status}`)
  }
  return res.json()
}

export const instancesQueryOptions = queryOptions({
  queryKey: ['instances'],
  queryFn: fetchInstances,
  refetchInterval: 3000,
})
