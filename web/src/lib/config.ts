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
  synced_issues?: number
}

// TrackerSwitchBlocker is one queue entry holding up a switch to the internal
// tracker, as the hub reports it on a 409.
export interface TrackerSwitchBlocker {
  repo: string
  id: string
  status: string
}

// ConfigWriteError carries the hub's structured refusal alongside its message so
// the editor can name the tickets in the way.
export class ConfigWriteError extends Error {
  reason?: string
  blocked?: TrackerSwitchBlocker[]

  constructor(
    message: string,
    reason?: string,
    blocked?: TrackerSwitchBlocker[],
  ) {
    super(message)
    this.name = 'ConfigWriteError'
    this.reason = reason
    this.blocked = blocked
  }
}

interface ConfigWriteFailure {
  error?: string
  reason?: string
  blocked?: TrackerSwitchBlocker[]
}

export async function writeFailure(
  res: Response,
  fallback = 'config write failed',
): Promise<ConfigWriteError> {
  const detail = (await res.json().catch(() => null)) as ConfigWriteFailure | null
  return new ConfigWriteError(
    detail?.error ?? `${fallback}: ${res.status}`,
    detail?.reason,
    detail?.blocked,
  )
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
    throw await writeFailure(res)
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
    throw await writeFailure(res)
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
