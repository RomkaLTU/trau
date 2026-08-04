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

/** A workspace the repo declares, in the three forms a run routes an app URL by. */
export interface Workspace {
  name: string
  path: string
  dir_name: string
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

async function fetchWorkspaces(repo: string): Promise<Workspace[]> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/workspaces`,
  )
  if (!res.ok) {
    throw new Error(`workspaces request failed: ${res.status}`)
  }
  const body = (await res.json()) as { workspaces?: Workspace[] }
  return body.workspaces ?? []
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

function workspaceForms(ws: Workspace): string[] {
  return [ws.name, ws.path, ws.dir_name].filter((form) => form !== '')
}

/** The value a suggestion inserts — the manifest name, or the path without one. */
export function workspaceSuggestion(ws: Workspace): string {
  return ws.name === '' ? ws.path : ws.name
}

export function filterWorkspaces(
  workspaces: readonly Workspace[],
  query: string,
): Workspace[] {
  const trimmed = query.trim().toLowerCase()
  if (trimmed === '') return [...workspaces]
  return workspaces.filter((ws) =>
    workspaceForms(ws).some((form) => form.toLowerCase().includes(trimmed)),
  )
}

/**
 * Whether a typed workspace routes nowhere: detection found workspaces and the
 * name is none of their forms. A blank name is the repo default, and a repo
 * with nothing detected says nothing about the name.
 */
export function workspaceUnrouted(
  value: string,
  workspaces: readonly Workspace[],
): boolean {
  const name = value.trim()
  if (name === '' || workspaces.length === 0) return false
  return !workspaces.some((ws) => workspaceForms(ws).includes(name))
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

export const workspacesQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['workspaces', repo],
    queryFn: () => fetchWorkspaces(repo),
    enabled: repo !== '',
    staleTime: 5 * 60_000,
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
