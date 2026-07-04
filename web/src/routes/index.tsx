import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { healthQueryOptions } from '@/lib/health'

export const Route = createFileRoute('/')({
  component: Overview,
  loader: ({ context }) => context.queryClient.ensureQueryData(healthQueryOptions),
})

function formatUptime(seconds: number): string {
  const s = Math.floor(seconds)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rem = s % 60
  if (h > 0) return `${h}h ${m}m ${rem}s`
  if (m > 0) return `${m}m ${rem}s`
  return `${rem}s`
}

function Overview() {
  const { data: health, error, isPending } = useQuery(healthQueryOptions)

  return (
    <Card className="max-w-md">
      <CardHeader>
        <CardTitle>Server health</CardTitle>
        <CardDescription>Live status of the trau serve hub</CardDescription>
      </CardHeader>
      <CardContent>
        {error && <p className="text-sm text-destructive">{String(error)}</p>}
        {isPending && !error && (
          <p className="text-sm text-muted-foreground">Loading…</p>
        )}
        {health && (
          <dl className="flex flex-col gap-3 text-sm">
            <div className="flex items-center justify-between">
              <dt className="text-muted-foreground">Status</dt>
              <dd>
                <Badge variant={health.status === 'ok' ? 'default' : 'destructive'}>
                  {health.status}
                </Badge>
              </dd>
            </div>
            <div className="flex items-center justify-between">
              <dt className="text-muted-foreground">Version</dt>
              <dd className="font-mono tabular-nums">{health.version}</dd>
            </div>
            <div className="flex items-center justify-between">
              <dt className="text-muted-foreground">Uptime</dt>
              <dd className="tabular-nums">{formatUptime(health.uptime_seconds)}</dd>
            </div>
          </dl>
        )}
      </CardContent>
    </Card>
  )
}
