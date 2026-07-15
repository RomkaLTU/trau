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
} from '@/lib/instances'
import { cn } from '@/lib/utils'

/**
 * Gates repo-scoped page content when the scoped repo can't serve it — it is
 * unconfigured, or its sync recorded a failure. Mirrors the ProjectScopeGate
 * overlay: children are dimmed and inert under a single designed message that
 * names the fix, instead of the raw fetch-error strings these pages would
 * otherwise print. Composes inside ProjectScopeGate, which stays outermost.
 */
export function RepoHealthGate({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  const { repo, repos } = useActiveRepo()
  const { data } = useQuery(repoHealthQueryOptions(repo ?? ''))

  const gate = data && healthBlocks(data.state) ? data : null
  const blocked = gate !== null

  return (
    <div className={cn('relative', className)}>
      <div
        inert={blocked || undefined}
        aria-hidden={blocked || undefined}
        className={cn(
          'transition-opacity duration-200',
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
            The page shows the last good data until sync recovers. This usually
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
