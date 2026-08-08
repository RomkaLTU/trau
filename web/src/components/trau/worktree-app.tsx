import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ExternalLink,
  Loader2,
  Play,
  RotateCw,
  Square,
  TriangleAlert,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { StatusPill, type RunState } from '@/components/trau/status-pill'
import type { Worktree } from '@/lib/rundetail'
import {
  appActionAvailable,
  appControlBlocked,
  controlWorktreeApp,
  worktreeAppLogsQueryOptions,
  type AppAction,
} from '@/lib/worktrees'

const APP_PILL: Record<string, { state: RunState; label: string }> = {
  running: { state: 'success', label: 'app running' },
  starting: { state: 'verify', label: 'app starting' },
  failed: { state: 'fail', label: 'app failed' },
  stopped: { state: 'todo', label: 'app stopped' },
}

const ACTION_ICON: Record<AppAction, typeof Play> = {
  start: Play,
  stop: Square,
  restart: RotateCw,
}

export function AppStateBadge({ worktree }: { worktree: Worktree }) {
  if (!worktree.serving) return null
  const pill = APP_PILL[worktree.app_state ?? 'stopped'] ?? APP_PILL.stopped
  return <StatusPill state={pill.state} label={pill.label} />
}

/** Where a running app answers — the link QA follows to this branch, not the shared checkout. */
export function AppURLLink({ worktree }: { worktree: Worktree }) {
  if (!worktree.app_url) return null
  return (
    <Button asChild size="sm" variant="outline" className="font-mono">
      <a href={worktree.app_url} target="_blank" rel="noreferrer">
        <ExternalLink className="size-3.5" aria-hidden="true" />
        app :{worktree.port}
      </a>
    </Button>
  )
}

/**
 * Start, Stop and Restart for the app one worktree serves. Every button is refused
 * with its reason while a run is working in the tree: bouncing a dev server under
 * the verify that is driving it turns a real verdict into a flake.
 */
export function WorktreeAppControls({
  repo,
  worktree,
  onSettled,
}: {
  repo: string
  worktree: Worktree
  onSettled?: () => void
}) {
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: (action: AppAction) =>
      controlWorktreeApp(repo, worktree.ticket, action),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['worktrees', repo] })
      void queryClient.invalidateQueries({
        queryKey: ['worktree-app-logs', repo, worktree.ticket],
      })
      onSettled?.()
    },
  })

  if (!worktree.serving) return null
  const blocked = appControlBlocked(worktree)
  const pending = mutation.isPending

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        {(['start', 'restart', 'stop'] as AppAction[])
          .filter((action) => appActionAvailable(worktree, action))
          .map((action) => {
            const Icon = ACTION_ICON[action]
            return (
              <Button
                key={action}
                size="sm"
                variant="outline"
                className="font-mono capitalize"
                title={blocked || undefined}
                disabled={pending || blocked !== ''}
                onClick={() => mutation.mutate(action)}
              >
                {pending && mutation.variables === action ? (
                  <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
                ) : (
                  <Icon className="size-3.5" aria-hidden="true" />
                )}
                {action === 'start' && worktree.app_state === 'failed'
                  ? 'retry'
                  : action}
              </Button>
            )
          })}
        <AppURLLink worktree={worktree} />
      </div>
      {blocked !== '' && (
        <p className="font-mono text-[0.7rem] text-muted-foreground">{blocked}</p>
      )}
      {mutation.isError && (
        <p
          className="inline-flex items-center gap-2 font-mono text-[0.7rem] text-fail"
          role="alert"
        >
          <TriangleAlert className="size-3.5" aria-hidden="true" />
          {mutation.error instanceof Error
            ? mutation.error.message
            : 'app control failed'}
        </p>
      )}
    </div>
  )
}

/**
 * The tail of what a tree's app printed, polled while the page is open. It is the
 * whole answer when a start command will not serve, so it is shown for a stopped
 * app too — the capture outlives the process that wrote it.
 */
export function WorktreeAppLogView({
  repo,
  ticket,
}: {
  repo: string
  ticket: string
}) {
  const { data, error, isPending } = useQuery(
    worktreeAppLogsQueryOptions(repo, ticket),
  )

  if (error) {
    return (
      <p
        className="inline-flex items-center gap-2 font-mono text-xs text-fail"
        role="alert"
      >
        <TriangleAlert className="size-3.5" aria-hidden="true" />
        {error instanceof Error ? error.message : 'app logs unavailable'}
      </p>
    )
  }
  if (isPending) {
    return (
      <p className="font-mono text-xs text-muted-foreground">loading app logs…</p>
    )
  }
  if (data.lines.length === 0) {
    return (
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        Nothing captured yet — this tree&apos;s app has not printed anything.
      </p>
    )
  }
  return (
    <div className="flex flex-col gap-1.5">
      {data.truncated && (
        <p className="font-mono text-[0.7rem] text-faint">
          showing the last {data.lines.length} lines
        </p>
      )}
      <pre className="scanlines max-h-72 overflow-auto rounded border border-border/60 bg-background/60 p-3 font-mono text-xs leading-relaxed text-foreground">
        {data.lines.join('\n')}
      </pre>
    </div>
  )
}
