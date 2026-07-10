import { useEffect, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { Boxes } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { EventFeed } from '@/components/event-feed'
import { RegisterRepoForm } from '@/components/register-repo-form'
import { RunControls } from '@/components/run-controls'
import { StatusPill } from '@/components/trau/status-pill'
import { UnregisterRepoButton } from '@/components/unregister-repo-button'
import { instancesQueryOptions, type Instance } from '@/lib/instances'
import { sessionStatePill, toSessionState } from '@/lib/overview'

export const Route = createFileRoute('/instances')({
  component: Instances,
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(instancesQueryOptions),
})

function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

function formatElapsed(fromISO: string, now: number): string {
  const s = Math.max(0, Math.floor((now - new Date(fromISO).getTime()) / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rem = s % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${rem}s`
  return `${rem}s`
}

function Instances() {
  const { data, error, isPending } = useQuery(instancesQueryOptions)
  const now = useNow(1000)

  const instances = data?.instances ?? []
  const allowedRepos = (data?.repos ?? []).filter((repo) => repo.allowed)
  const idleRepos = (data?.repos ?? []).filter((repo) => !repo.live)

  return (
    <div className="flex flex-col gap-6">
      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}

      {data && instances.length === 0 && (
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle>Instances</CardTitle>
            <CardDescription>
              No trau loops are running on this machine right now.
            </CardDescription>
          </CardHeader>
        </Card>
      )}

      {instances.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2">
          {instances.map((instance) => (
            <InstanceCard key={instance.pid} instance={instance} now={now} />
          ))}
        </div>
      )}

      {data && (
        <section className="flex flex-col gap-3">
          <h2 className="text-sm font-medium text-muted-foreground">
            Add a repo
          </h2>
          <div className="max-w-md">
            <RegisterRepoForm />
          </div>
        </section>
      )}

      {allowedRepos.length > 0 && (
        <section className="flex flex-col gap-3">
          <h2 className="text-sm font-medium text-muted-foreground">
            Start a run
          </h2>
          <div className="grid gap-4 sm:grid-cols-2">
            {allowedRepos.map((repo) => (
              <div key={repo.root} className="flex flex-col gap-2">
                <RunControls repo={repo.name} />
                {repo.registered && <UnregisterRepoButton repo={repo.name} />}
              </div>
            ))}
          </div>
        </section>
      )}

      {idleRepos.length > 0 && (
        <section className="flex flex-col gap-2">
          <h2 className="text-sm font-medium text-muted-foreground">
            Previously seen repos
          </h2>
          <div className="flex flex-wrap gap-2">
            {idleRepos.map((repo) => (
              <Badge key={repo.root} variant="outline" title={repo.root}>
                {repo.name}
              </Badge>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

function InstanceCard({
  instance,
  now,
}: {
  instance: Instance
  now: number
}) {
  const state = toSessionState(instance.session_state)
  const pill = sessionStatePill(state)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <Boxes className="size-4 text-muted-foreground" />
          {instance.repo}
          <StatusPill state={pill.state} label={pill.label} />
        </CardTitle>
        <CardDescription className="font-mono">PID {instance.pid}</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="flex flex-col gap-3 text-sm">
          {instance.ticket ? (
            <>
              <Row label="Ticket">
                <span className="font-mono">{instance.ticket}</span>
              </Row>
              {state === 'working' && instance.phase && (
                <Row label="Phase">
                  <Badge variant="secondary">{instance.phase}</Badge>
                </Row>
              )}
              {instance.state_since && (
                <Row label="In state">
                  <span className="tabular-nums">
                    {formatElapsed(instance.state_since, now)}
                  </span>
                </Row>
              )}
            </>
          ) : (
            <Row label="Status">
              <span className="text-muted-foreground">Idle — between tickets</span>
            </Row>
          )}
          <Row label="Started">
            <span className="tabular-nums">
              {new Date(instance.started_at).toLocaleString()}
            </span>
          </Row>
        </dl>
        <EventFeed repo={instance.repo} limit={5} className="mt-4" />
      </CardContent>
    </Card>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}
