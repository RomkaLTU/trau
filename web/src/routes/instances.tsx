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
import { instancesQueryOptions, type Instance } from '@/lib/instances'

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
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Boxes className="size-4 text-muted-foreground" />
          {instance.repo}
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
              <Row label="Phase">
                <Badge variant="secondary">{instance.phase}</Badge>
              </Row>
              {instance.phase_since && (
                <Row label="Elapsed">
                  <span className="tabular-nums">
                    {formatElapsed(instance.phase_since, now)}
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
