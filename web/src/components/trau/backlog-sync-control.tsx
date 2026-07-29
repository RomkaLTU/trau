import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCw } from 'lucide-react'

import { invalidateRepoBoard } from '@/lib/backlog'
import { syncedAgo, useNow } from '@/lib/elapsed'
import {
  repoHealthQueryOptions,
  syncRepo,
  type RepoHealthState,
  type SyncResponse,
} from '@/lib/instances'
import { cn } from '@/lib/utils'

// How long the pull counts stay up before the line settles back to the relative
// time — long enough to read, short enough not to look stuck.
const COUNTS_HOLD_MS = 4000

export interface SyncStatus {
  state?: RepoHealthState
  lastSyncedAt?: string
  lastError?: string
  pending: boolean
  error?: string
  pulled?: SyncResponse
  now: number
}

export interface SyncStatusLine {
  text: string
  failed: boolean
}

export function syncStatusLine(status: SyncStatus): SyncStatusLine {
  if (status.pending || status.state === 'syncing') {
    return { text: 'syncing…', failed: false }
  }
  const failure = status.error ?? status.lastError
  if (failure) return { text: failure, failed: true }
  if (status.pulled) return { text: pullCounts(status.pulled), failed: false }
  if (!status.lastSyncedAt) return { text: 'never synced', failed: false }
  return {
    text: `synced ${syncedAgo(status.lastSyncedAt, status.now)}`,
    failed: false,
  }
}

function pullCounts(res: SyncResponse): string {
  const counts = `pulled ${res.issues} issues · ${res.comments} comments`
  return res.removed > 0 ? `${counts} · removed ${res.removed}` : counts
}

/**
 * Pulls the repo's tracker on demand and keeps its sync status in view, so a
 * stale or failing backlog is visible without leaving the page. Hidden for a repo
 * with no external tracker: there is nothing to pull and the endpoint would
 * refuse. A failure shows the hub's own message here and leaves the blocking
 * RepoHealthGate overlay to take it from there.
 */
export function BacklogSyncControl({ repo }: { repo: string }) {
  // The pull counts and the mutation's error belong to the repo they came from;
  // remounting on a repo switch is what stops them following the user to the next.
  return <RepoSyncControl key={repo} repo={repo} />
}

function RepoSyncControl({ repo }: { repo: string }) {
  const queryClient = useQueryClient()
  const health = useQuery(repoHealthQueryOptions(repo))
  const now = useNow(1000)
  const [pulled, setPulled] = useState<SyncResponse | undefined>()

  const sync = useMutation({
    mutationFn: () => syncRepo(repo),
    onSuccess: setPulled,
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['repo-health', repo] })
      invalidateRepoBoard(queryClient, repo)
    },
  })

  useEffect(() => {
    if (!pulled) return
    const id = setTimeout(() => setPulled(undefined), COUNTS_HOLD_MS)
    return () => clearTimeout(id)
  }, [pulled])

  const provider = health.data?.provider
  if (!provider || provider === 'internal') return null

  const syncing = health.data?.state === 'syncing'
  const line = syncStatusLine({
    state: health.data?.state,
    lastSyncedAt: health.data?.last_synced_at,
    lastError: health.data?.last_error,
    pending: sync.isPending,
    error: sync.error?.message,
    pulled,
    now,
  })

  return (
    <div className="flex items-center gap-2">
      <span
        className={cn(
          'whitespace-nowrap font-mono text-xs',
          line.failed
            ? 'max-w-64 truncate text-fail'
            : 'text-muted-foreground',
        )}
        title={line.failed ? line.text : undefined}
      >
        {line.text}
      </span>
      <button
        type="button"
        onClick={() => sync.mutate()}
        disabled={sync.isPending || syncing}
        className="inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-sm text-foreground transition-colors hover:bg-muted disabled:opacity-50"
      >
        <RotateCw className="size-4" />
        Sync
      </button>
    </div>
  )
}
