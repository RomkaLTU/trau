import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export interface ConfigKey {
  key: string
  value: string
  layer: string
  group?: string
  kind?: string
  default?: string
  description?: string
  options?: string[]
  suggestions?: string[]
  bool?: boolean
  advanced?: boolean
  tracker?: string
  editable: boolean
  secret?: boolean
  set?: boolean
  disabled_reason?: string
}

export interface ConfigResponse {
  repo: string
  layers: string[]
  providers: string[]
  keys: ConfigKey[]
}

// ConfigScope is the repo a settings surface edits, or null for the global
// defaults every project inherits from ~/.trau.ini.
export type ConfigScope = string | null

export interface ConfigWrite {
  key: string
  value: string
  layer: string
  unset?: boolean
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

async function fetchGlobalConfig(): Promise<ConfigResponse> {
  const res = await apiFetch('/api/v1/config')
  if (!res.ok) {
    throw new Error(`config request failed: ${res.status}`)
  }
  return res.json()
}

export async function writeGlobalConfig(
  body: ConfigWrite,
): Promise<ConfigKey> {
  const res = await apiFetch('/api/v1/config', {
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

export const globalConfigQueryOptions = queryOptions({
  queryKey: ['config', 'global'],
  queryFn: fetchGlobalConfig,
})

export function configScopeQueryOptions(scope: ConfigScope) {
  return scope === null ? globalConfigQueryOptions : configQueryOptions(scope)
}

export function configScopeKey(scope: ConfigScope): string[] {
  return ['config', scope ?? 'global']
}

export function writeConfigIn(
  scope: ConfigScope,
  body: ConfigWrite,
): Promise<ConfigKey> {
  return scope === null ? writeGlobalConfig(body) : writeConfig(scope, body)
}
