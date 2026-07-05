import { queryOptions } from '@tanstack/react-query'

import { apiFetch } from './api'

export type EpicSubState = 'done' | 'epic' | 'todo'

export interface EpicSubIssue {
  id: string
  title: string
  state: EpicSubState
}

export interface EpicPreviewResult {
  repo: string
  repo_root: string
  epic: string
  sub_issues: EpicSubIssue[]
}

const TICKET_ID = /^[A-Za-z][A-Za-z0-9_]*-\d+$/

export function isTicketId(id: string): boolean {
  return TICKET_ID.test(id.trim())
}

async function fetchEpicPreview(
  repo: string,
  epic: string,
): Promise<EpicPreviewResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/epics/${encodeURIComponent(epic)}`,
  )
  if (!res.ok) {
    const detail = (await res.json().catch(() => null)) as {
      error?: string
    } | null
    throw new Error(detail?.error ?? `epic preview failed: ${res.status}`)
  }
  return res.json()
}

export const epicPreviewQueryOptions = (repo: string, epic: string) =>
  queryOptions({
    queryKey: ['epic-preview', repo, epic],
    queryFn: () => fetchEpicPreview(repo, epic),
    enabled: repo !== '' && isTicketId(epic),
    staleTime: 30_000,
  })
