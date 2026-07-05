import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface ConfigKey {
  key: string
  value: string
  layer: string
  default?: string
  description?: string
  options?: string[]
  bool?: boolean
  advanced?: boolean
  editable: boolean
  secret?: boolean
  set?: boolean
}

export interface ConfigResponse {
  repo: string
  layers: string[]
  providers: string[]
  keys: ConfigKey[]
}

export interface ConfigWrite {
  key: string
  value: string
  layer: string
}

async function fetchConfig(repo: string): Promise<ConfigResponse> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/config`)
  if (!res.ok) {
    throw new Error(`config request failed: ${res.status}`)
  }
  return res.json()
}

export async function writeConfig(
  repo: string,
  body: ConfigWrite,
): Promise<ConfigKey> {
  const res = await apiFetch(`/api/v1/repos/${encodeURIComponent(repo)}/config`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `config write failed: ${res.status}`)
  }
  return res.json()
}

export const configQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['config', repo],
    queryFn: () => fetchConfig(repo),
    enabled: repo !== '',
  })
