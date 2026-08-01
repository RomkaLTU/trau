import { queryOptions, useQuery } from '@tanstack/react-query'

import { apiFetch } from './api'
import { configQueryOptions, type ConfigKey } from './config'

export interface AppURL {
  id: number
  label: string
  url: string
  workspace: string
}

export interface AppURLDraft {
  label: string
  url: string
  workspace: string
}

export interface AppURLFallback {
  workspace: string
  url: string
}

async function fetchAppURLs(repo: string): Promise<AppURL[]> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/app-urls`,
  )
  if (!res.ok) {
    throw new Error(`app urls request failed: ${res.status}`)
  }
  return res.json()
}

async function send(url: string, init: RequestInit, action: string): Promise<Response> {
  const res = await apiFetch(url, init)
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `${action} failed: ${res.status}`)
  }
  return res
}

function jsonBody(body: unknown): RequestInit {
  return {
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }
}

export async function createAppURL(
  repo: string,
  draft: AppURLDraft,
): Promise<AppURL> {
  const res = await send(
    `/api/v1/repos/${encodeURIComponent(repo)}/app-urls`,
    { method: 'POST', ...jsonBody(draft) },
    'app url create',
  )
  return res.json()
}

export async function updateAppURL(
  repo: string,
  id: number,
  draft: AppURLDraft,
): Promise<void> {
  await send(
    `/api/v1/repos/${encodeURIComponent(repo)}/app-urls/${id}`,
    { method: 'PUT', ...jsonBody(draft) },
    'app url update',
  )
}

export async function deleteAppURL(repo: string, id: number): Promise<void> {
  await send(
    `/api/v1/repos/${encodeURIComponent(repo)}/app-urls/${id}`,
    { method: 'DELETE' },
    'app url delete',
  )
}

export function draftFor(entry: AppURL | null): AppURLDraft {
  return {
    label: entry?.label ?? '',
    url: entry?.url ?? '',
    workspace: entry?.workspace ?? '',
  }
}

export function workspaceLabel(workspace: string): string {
  return workspace === '' ? 'default' : workspace
}

/** `editing` is the id being replaced, or null for a new entry. */
export function draftIssue(
  draft: AppURLDraft,
  entries: readonly AppURL[],
  editing: number | null,
): string | null {
  if (draft.url.trim() === '') return 'a URL is required'
  const workspace = draft.workspace.trim()
  const clash = entries.some(
    (e) => e.id !== editing && e.workspace === workspace,
  )
  if (!clash) return null
  if (workspace === '') {
    return 'a default app URL already exists for this repo — give this entry a workspace or edit the existing one'
  }
  return `an app URL for workspace “${workspace}” already exists`
}

/** Parses APP_URLS — comma-separated <workspace>=<url> pairs — dropping half pairs. */
export function parseAppURLsValue(value: string): AppURLFallback[] {
  const out: AppURLFallback[] = []
  for (const pair of value.split(',')) {
    const sep = pair.indexOf('=')
    if (sep < 0) continue
    const workspace = pair.slice(0, sep).trim()
    const url = pair.slice(sep + 1).trim()
    if (workspace === '' || url === '') continue
    out.push({ workspace, url })
  }
  return out.sort((a, b) => a.workspace.localeCompare(b.workspace))
}

/** The ini targets, default first, as a hub-less repo still resolves them. */
export function configFallback(keys: readonly ConfigKey[]): AppURLFallback[] {
  const valueOf = (key: string) =>
    keys.find((k) => k.key === key)?.value.trim() ?? ''
  const fallback = parseAppURLsValue(valueOf('APP_URLS'))
  const single = valueOf('APP_URL')
  if (single !== '') fallback.unshift({ workspace: '', url: single })
  return fallback
}

export const appURLsQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['app-urls', repo],
    queryFn: () => fetchAppURLs(repo),
    enabled: repo !== '',
  })

export interface AppURLTargets {
  entries: AppURL[]
  /** Only populated while the repo has no entries — the hub wins wholesale. */
  fallback: AppURLFallback[]
  isPending: boolean
  error: Error | null
  refetch: () => void
}

/**
 * Reads the repo's browser targets the way a run resolves them: the hub
 * entries, or — only when there are none — the ini values, fetched lazily so
 * the common case costs one request.
 */
export function useAppURLTargets(repo: string): AppURLTargets {
  const entriesQuery = useQuery(appURLsQueryOptions(repo))
  const entries = entriesQuery.data ?? []
  const empty = entriesQuery.isSuccess && entries.length === 0
  const configQuery = useQuery({ ...configQueryOptions(repo), enabled: empty })

  return {
    entries,
    fallback: empty ? configFallback(configQuery.data?.keys ?? []) : [],
    isPending: entriesQuery.isPending || (empty && configQuery.isPending),
    error: entriesQuery.error,
    refetch: () => {
      void entriesQuery.refetch()
    },
  }
}
