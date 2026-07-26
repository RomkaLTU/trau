import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { TerminalCard } from '@/components/trau/terminal-card'
import {
  lastSyncLabel,
  matchesTeamSync,
  syncTeamNow,
  teamSyncQueryOptions,
  type TeamSyncStatus,
} from '@/lib/teamsync'

export function TeamSyncSection({
  repo,
  query = '',
}: {
  repo: string
  query?: string
}) {
  const queryClient = useQueryClient()
  const { data, error, isPending } = useQuery(teamSyncQueryOptions(repo))
  const sync = useMutation({
    mutationFn: () => syncTeamNow(repo),
    onSuccess: (status) => {
      queryClient.setQueryData(teamSyncQueryOptions(repo).queryKey, status)
      void queryClient.invalidateQueries({ queryKey: ['lessons', repo] })
      toast(status.last_error ? 'Sync finished with errors' : 'Sync complete')
    },
    onError: (err: Error) => toast.error(err.message),
  })

  if (!matchesTeamSync(query)) return null

  return (
    <section id="team-sync-status" className="scroll-mt-6">
      <TerminalCard title="Team sync status" bodyClassName="p-0">
        <div className="flex flex-col">
          <p className="border-b border-border/60 px-4 py-2 text-xs leading-relaxed text-muted-foreground">
            Lessons travel over this repo's git remote as a snapshot on
            refs/trau/team/. Sync is best-effort: a failure here never affects a
            run.
          </p>
          {error && (
            <p
              className="inline-flex items-center gap-2 px-4 py-3 font-mono text-xs text-fail"
              role="alert"
            >
              <TriangleAlert className="size-3.5" aria-hidden="true" />
              {error.message}
            </p>
          )}
          {isPending && !error && (
            <p className="px-4 py-3 font-mono text-xs text-muted-foreground">
              Loading…
            </p>
          )}
          {data && <StatusBody status={data} />}
          <div className="flex items-center gap-3 border-t border-border/60 px-4 py-3">
            <Button
              variant="outline"
              size="sm"
              disabled={!data?.enabled || sync.isPending}
              onClick={() => sync.mutate()}
            >
              <RefreshCw
                className={sync.isPending ? 'animate-spin' : undefined}
                aria-hidden="true"
              />
              Sync now
            </Button>
            {!data?.enabled && (
              <span className="font-mono text-xs text-faint">
                {data?.has_remote === false
                  ? 'no git remote on this repo'
                  : 'set TEAM_SYNC=1 to share lessons'}
              </span>
            )}
          </div>
        </div>
      </TerminalCard>
    </section>
  )
}

function StatusBody({ status }: { status: TeamSyncStatus }) {
  return (
    <dl className="flex flex-col">
      <Row label="sharing">{status.enabled ? 'on' : 'off'}</Row>
      <Row label="remote">
        {status.has_remote ? status.remote : 'none configured'}
      </Row>
      <Row label="teammates">{String(status.teammates)}</Row>
      <Row label="publishing as">
        {status.writer_id
          ? `${status.author || 'unnamed'} · ${status.writer_id}`
          : 'not yet published'}
      </Row>
      <Row label="last sync">{lastSyncLabel(status)}</Row>
      {status.last_error && (
        <Row label="last error" tone="fail">
          {status.last_error}
        </Row>
      )}
    </dl>
  )
}

function Row({
  label,
  tone,
  children,
}: {
  label: string
  tone?: 'fail'
  children: React.ReactNode
}) {
  return (
    <div className="flex gap-3 px-4 py-1.5 first:pt-3">
      <dt className="w-32 shrink-0 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </dt>
      <dd
        className={
          tone === 'fail'
            ? 'min-w-0 break-words font-mono text-xs text-fail'
            : 'min-w-0 break-words font-mono text-xs text-foreground'
        }
      >
        {children}
      </dd>
    </div>
  )
}
