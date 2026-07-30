import type { ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { RotateCw, TriangleAlert, Wrench, X } from 'lucide-react'

import { useActiveRepo } from '@/components/trau/active-repo'
import { Button } from '@/components/ui/button'
import {
  healthBlocks,
  repoHealthQueryOptions,
  syncRepo,
  type RepoHealth,
  type SyncErrorKind,
} from '@/lib/instances'
import { isDismissed, useDismissals } from '@/lib/sync-dismissed'
import { cn } from '@/lib/utils'

/**
 * Gates repo-scoped page content when the repo behind it can't serve it — it is
 * unconfigured, or every sync it ever ran failed. Mirrors the ProjectScopeGate
 * overlay: children are dimmed and inert under a single designed message that
 * names the fix, instead of the raw fetch-error strings these pages would
 * otherwise print. Composes inside ProjectScopeGate, which stays outermost.
 * Defaults to the scoped repo; a page reading another repo's tracker passes the
 * repo it actually reads.
 *
 * A repo that synced once and is failing now is degraded, not gated: the store
 * still holds that pull, so the page stays interactive under a dismissable notice.
 */
export function RepoHealthGate({
  children,
  className,
  repo: gated,
}: {
  children: ReactNode
  className?: string
  repo?: string
}) {
  const { repo, repos } = useActiveRepo()
  const { data } = useQuery(repoHealthQueryOptions(gated ?? repo ?? ''))
  const [dismissals, dismiss] = useDismissals()

  const gate = data && healthBlocks(data.state) ? data : null
  const blocked = gate !== null
  const degraded =
    data?.state === 'degraded' &&
    !isDismissed(dismissals, data.repo, data.last_error)
      ? data
      : null

  return (
    <div className={cn('relative flex flex-col', className)}>
      {degraded && (
        <SyncDegradedBanner
          health={degraded}
          root={repos.find((r) => r.name === degraded.repo)?.root}
          onDismiss={() => dismiss(degraded.repo, degraded.last_error)}
        />
      )}

      <div
        inert={blocked || undefined}
        aria-hidden={blocked || undefined}
        className={cn(
          'relative min-h-0 flex-1 transition-opacity duration-200',
          blocked && 'pointer-events-none select-none opacity-40 blur-[1px]',
        )}
      >
        {children}
      </div>

      {gate && (
        <div
          role="status"
          className="absolute inset-0 z-10 flex items-start justify-center pt-16"
        >
          <HealthGateCard
            health={gate}
            root={repos.find((r) => r.name === gate.repo)?.root}
          />
        </div>
      )}
    </div>
  )
}

export interface DegradedNotice {
  headline: string
  hint: string
  fixable: boolean
}

// degradedNotice says what a still-serving repo is waiting on. A failure that clears
// itself gets no configuration prompt: pointing at the setup wizard would send the
// user after a fault that is not theirs.
export function degradedNotice(
  repo: string,
  kind: SyncErrorKind,
): DegradedNotice {
  switch (kind) {
    case 'rate-limit':
      return {
        headline: `${repo}'s tracker is rate-limiting`,
        hint: 'Sync retries automatically once the request budget refills. The board below is the last good data.',
        fixable: false,
      }
    case 'transient':
      return {
        headline: `${repo}'s tracker is unreachable`,
        hint: 'Sync retries automatically. The board below is the last good data.',
        fixable: false,
      }
    default:
      return {
        headline: `Sync is failing for ${repo}`,
        hint: 'The board below is the last good data. This usually means the tracker provider or credentials are wrong.',
        fixable: true,
      }
  }
}

function SyncDegradedBanner({
  health,
  root,
  onDismiss,
}: {
  health: RepoHealth
  root?: string
  onDismiss: () => void
}) {
  const notice = degradedNotice(health.repo, health.last_error_kind)

  return (
    <div
      role="status"
      className="mb-4 flex shrink-0 items-start justify-between gap-3 rounded-lg border border-warn/50 bg-warn/12 px-4 py-3"
    >
      <div className="flex min-w-0 items-start gap-2.5">
        <TriangleAlert
          className="mt-0.5 size-4 shrink-0 text-warn"
          aria-hidden="true"
        />
        <div className="flex min-w-0 flex-col gap-1">
          <p className="font-mono text-sm leading-relaxed text-warn">
            {notice.headline}
          </p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            {notice.hint}
          </p>
          {health.last_error && (
            <p className="truncate font-mono text-xs text-warn/80">
              {health.last_error}
            </p>
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {notice.fixable && (
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/projects/new" search={{ path: root }}>
              <Wrench data-icon="inline-start" aria-hidden="true" />
              Fix configuration
            </Link>
          </Button>
        )}
        <button
          type="button"
          onClick={onDismiss}
          aria-label="Dismiss"
          className="flex size-6 items-center justify-center rounded-md text-warn/80 hover:bg-warn/12 hover:text-warn"
        >
          <X className="size-4" aria-hidden="true" />
        </button>
      </div>
    </div>
  )
}

function HealthGateCard({
  health,
  root,
}: {
  health: RepoHealth
  root?: string
}) {
  const failed = health.state === 'sync-failed'

  return (
    <div className="flex max-w-md flex-col items-center gap-3 rounded-md border border-border bg-popover px-6 py-5 text-center shadow-lg">
      <span
        className={cn(
          'flex size-9 items-center justify-center rounded-md border',
          failed
            ? 'border-fail/50 bg-fail/10 text-fail'
            : 'border-warn/50 bg-warn/10 text-warn',
        )}
        aria-hidden="true"
      >
        {failed ? <X className="size-4" /> : <TriangleAlert className="size-4" />}
      </span>

      {failed ? (
        <>
          <p className="font-mono text-sm text-foreground">
            Sync is failing for {health.repo}
          </p>
          {health.last_error && (
            <p className="w-full truncate rounded border border-fail/30 bg-fail/5 px-2.5 py-1.5 font-mono text-xs text-fail">
              {health.last_error}
            </p>
          )}
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            No sync has landed yet, so there is nothing to show. This usually
            means the tracker provider or credentials are wrong.
          </p>
        </>
      ) : (
        <>
          <p className="font-mono text-sm text-foreground">
            {`${health.repo} isn't configured yet`}
          </p>
          <p className="font-sans text-xs leading-relaxed text-muted-foreground">
            The repo is registered, but has no tracker or project binding — so
            there is nothing to show. Finish setup and the issues will appear
            here.
          </p>
        </>
      )}

      <HealthGateActions repo={health.repo} root={root} failed={failed} />
    </div>
  )
}

function HealthGateActions({
  repo,
  root,
  failed,
}: {
  repo: string
  root?: string
  failed: boolean
}) {
  const queryClient = useQueryClient()
  const sync = useMutation({
    mutationFn: () => syncRepo(repo),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['repo-health', repo] })
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['backlog', repo] })
    },
  })

  return (
    <div className="mt-1 flex flex-col items-center gap-1.5">
      <div className="flex flex-wrap items-center justify-center gap-2">
        <Button asChild size="sm" className="font-mono">
          <Link to="/projects/new" search={{ path: root }}>
            <Wrench data-icon="inline-start" aria-hidden="true" />
            Fix configuration
          </Link>
        </Button>
        {failed && (
          <Button
            variant="outline"
            size="sm"
            className="font-mono"
            disabled={sync.isPending}
            onClick={() => sync.mutate()}
          >
            <RotateCw data-icon="inline-start" aria-hidden="true" />
            {sync.isPending ? 'Syncing…' : 'Retry sync'}
          </Button>
        )}
      </div>
      {sync.error && (
        <p className="font-mono text-xs text-fail">
          {(sync.error as Error).message}
        </p>
      )}
    </div>
  )
}
