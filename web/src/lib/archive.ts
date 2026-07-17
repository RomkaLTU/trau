import { useMutation, useQueryClient } from '@tanstack/react-query'

import { apiFetch } from './api'
import { issueQueryOptions, type Issue } from './issues'

// ArchiveResult is the archive endpoint's answer: the updated issue plus how many
// pending queue entries the archive pruned, so the caller can toast the removal.
// queue_removed is zero on an unarchive.
export interface ArchiveResult extends Issue {
  queue_removed: number
}

async function errorMessage(res: Response, fallback: string): Promise<string> {
  const detail = (await res.json().catch(() => null)) as { error?: string } | null
  return detail?.error ?? `${fallback}: ${res.status}`
}

export async function archiveIssue(
  repo: string,
  id: string,
  archived: boolean,
): Promise<ArchiveResult> {
  const res = await apiFetch(
    `/api/v1/repos/${encodeURIComponent(repo)}/issues/${encodeURIComponent(id)}/archive`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ archived }),
    },
  )
  if (!res.ok) {
    throw new Error(await errorMessage(res, 'archive failed'))
  }
  return res.json()
}

// archiveToastMessage phrases the confirmation for a completed archive: naming the
// issue, and — on an archive that pruned queued work — how many pending entries
// went with it.
export function archiveToastMessage(
  id: string,
  archived: boolean,
  queueRemoved: number,
): string {
  if (!archived) return `Unarchived ${id}`
  if (queueRemoved > 0) {
    const noun = queueRemoved === 1 ? 'queued item' : 'queued items'
    return `Archived ${id} — removed ${queueRemoved} ${noun}`
  }
  return `Archived ${id}`
}

export interface ArchiveVars {
  id: string
  archived: boolean
}

// useArchiveIssue wires the archive mutation shared by the board rows and the
// drawer: it refreshes the mutated issue in place, then invalidates the backlog
// (the row leaves or joins the archived view) and the queue (archiving prunes the
// issue's pending entries server-side), and hands the outcome to onArchived so the
// caller can toast it.
export function useArchiveIssue(
  repo: string,
  onArchived: (result: ArchiveResult, vars: ArchiveVars) => void,
) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (vars: ArchiveVars) => archiveIssue(repo, vars.id, vars.archived),
    onSuccess: (result, vars) => {
      client.setQueryData(issueQueryOptions(repo, vars.id).queryKey, result)
      void client.invalidateQueries({ queryKey: ['backlog', repo] })
      void client.invalidateQueries({ queryKey: ['queue', repo] })
      onArchived(result, vars)
    },
  })
}
