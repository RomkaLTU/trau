import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'
import { formatAge } from './ledger'
import type { Worktree, WorktreeApp } from './rundetail'

/** The tail of a worktree app's captured output, oldest line first. */
export interface WorktreeAppLogs {
  ticket: string
  lines: string[]
  /** True when the capture holds more than the tail carried here. */
  truncated: boolean
}

export type AppAction = 'start' | 'stop' | 'restart'

/** How many log lines the viewer tails per poll. */
export const APP_LOG_LINES = 200

function repoPath(repo: string): string {
  return `/api/v1/repos/${encodeURIComponent(repo)}/worktrees`
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

async function fetchWorktrees(repo: string): Promise<Worktree[]> {
  const res = await send(repoPath(repo), {}, 'worktrees request')
  return res.json()
}

export const worktreesQueryOptions = (repo: string) =>
  queryOptions({
    queryKey: ['worktrees', repo],
    queryFn: () => fetchWorktrees(repo),
    refetchInterval: 5000,
    enabled: repo !== '',
  })

async function fetchWorktreeAppLogs(
  repo: string,
  ticket: string,
  lines: number,
): Promise<WorktreeAppLogs> {
  const res = await send(
    `${repoPath(repo)}/${encodeURIComponent(ticket)}/app/logs?n=${lines}`,
    {},
    'app logs request',
  )
  const view = (await res.json()) as WorktreeAppLogs
  return { ...view, lines: view.lines ?? [] }
}

export const worktreeAppLogsQueryOptions = (
  repo: string,
  ticket: string,
  lines = APP_LOG_LINES,
) =>
  queryOptions({
    queryKey: ['worktree-app-logs', repo, ticket, lines],
    queryFn: () => fetchWorktreeAppLogs(repo, ticket, lines),
    refetchInterval: 3000,
    enabled: repo !== '' && ticket !== '',
  })

/**
 * Starts, stops or restarts the app one worktree serves. The hub waits for a
 * started app's port to accept before answering, so a start can sit pending for as
 * long as a cold dev server takes to boot, and it refuses outright while a run is
 * working in the tree.
 */
export async function controlWorktreeApp(
  repo: string,
  ticket: string,
  action: AppAction,
): Promise<WorktreeApp> {
  const res = await send(
    `${repoPath(repo)}/${encodeURIComponent(ticket)}/app/${action}`,
    { method: 'POST' },
    `app ${action}`,
  )
  return res.json()
}

/**
 * Removes a worktree from disk and settles its row. The hub refuses while a run is
 * still live in the repo, which surfaces here as that refusal's own message.
 */
export async function removeWorktree(
  repo: string,
  id: number,
): Promise<Worktree> {
  const res = await send(
    `${repoPath(repo)}/${id}`,
    { method: 'DELETE' },
    'remove worktree',
  )
  return res.json()
}

/**
 * How long a tree has been standing, in the relative words the rest of the app
 * uses. An unparseable stamp is handed back as it came rather than rendered as an
 * age nothing supports.
 */
export function worktreeAge(createdAt: string, now = Date.now()): string {
  const created = Date.parse(createdAt)
  if (Number.isNaN(created)) return createdAt
  const age = formatAge(Math.max(0, now - created))
  return age === 'just now' ? 'created just now' : `created ${age} ago`
}

/** The trees still on disk — the only ones with an app to operate or a tree to remove. */
export function activeWorktrees(rows: readonly Worktree[]): Worktree[] {
  return rows.filter((row) => row.state === 'active')
}

/**
 * Why an app control is refused, empty when it is not. A run working in the tree
 * owns its app; a tree that has left the disk has no app to operate; and a repo
 * with no APP_START_CMD serves nothing to operate in the first place.
 */
export function appControlBlocked(worktree: Worktree): string {
  if (worktree.state !== 'active') {
    return 'this tree is no longer on disk'
  }
  if (!worktree.serving) {
    return 'this repo has no APP_START_CMD, so its worktrees serve no app'
  }
  return worktree.busy ?? ''
}

/** Whether an action is offered at all for the app's current standing. */
export function appActionAvailable(
  worktree: Worktree,
  action: AppAction,
): boolean {
  const state = worktree.app_state ?? 'stopped'
  if (action === 'start') return state !== 'running' && state !== 'starting'
  return state === 'running' || state === 'starting'
}
