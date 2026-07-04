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
  allowed: boolean
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

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export interface StartRequest {
  repo: string
  ticket?: string
  epic?: string
  provider?: string
}

export interface StartResult {
  pid: number
  repo: string
  repo_root: string
}

export async function startInstance(req: StartRequest): Promise<StartResult> {
  const res = await fetch('/api/v1/instances', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'start failed'))
  }
  return res.json()
}

export interface DryRunResult {
  repo: string
  repo_root: string
  ticket: string
}

export async function dryRun(repo: string): Promise<DryRunResult> {
  const res = await fetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/dry-run`,
    { method: 'POST' },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'dry-run failed'))
  }
  return res.json()
}
