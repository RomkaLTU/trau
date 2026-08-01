import { useEffect, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { Boxes, FolderGit2, Plus, RotateCw, Wrench } from 'lucide-react'

import { EventFeed } from '@/components/event-feed'
import { MakeStartableButton } from '@/components/make-startable-button'
import { RemoveProjectButton } from '@/components/remove-project-button'
import { RemoveRepoButton } from '@/components/remove-repo-button'
import { PageHeader } from '@/components/trau/page-header'
import { StatusPill } from '@/components/trau/status-pill'
import { TerminalCard } from '@/components/trau/terminal-card'
import { UnregisterRepoButton } from '@/components/unregister-repo-button'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  anySyncing,
  healthPill,
  instancesQueryOptions,
  removeBlocked,
  repoHealth,
  syncRepo,
  type Instance,
  type RepoHealthState,
  type RepoView,
} from '@/lib/instances'
import { sessionStatePill, toSessionState } from '@/lib/overview'
import { standardTitle, usePageTitle } from '@/lib/page-title'
import {
  groupRepos,
  projectsQueryOptions,
  type ProjectView,
} from '@/lib/projects'
import { reposQueryOptions } from '@/lib/runs'
import { cn } from '@/lib/utils'

const SYNC_POLL_MS = 2000

export const Route = createFileRoute('/instances')({
  component: Instances,
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(instancesQueryOptions),
      context.queryClient.ensureQueryData(reposQueryOptions),
      context.queryClient.ensureQueryData(projectsQueryOptions),
    ]),
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
  usePageTitle(standardTitle('Instances'))
  const { data, error, isPending } = useQuery(instancesQueryOptions)
  // A pull settles out of band, so follow it to completion and then stop.
  const repos = useQuery({
    ...reposQueryOptions,
    refetchInterval: (query) =>
      anySyncing(query.state.data?.repos ?? []) ? SYNC_POLL_MS : false,
  })
  const projects = useQuery(projectsQueryOptions)
  const now = useNow(1000)

  const instances = data?.instances ?? []
  const repoViews = repos.data?.repos ?? []
  // In server order, so groupRepos seats each group where its highest-ranked member
  // sat and the page's ordering carries over.
  const rows = groupRepos(repoViews, projects.data?.projects ?? [])

  return (
    <>
      <PageHeader
        eyebrow="instances"
        title="Instances"
        description="Live loops on this machine, and the health of every repo the hub knows about."
        actions={
          <Button asChild size="sm" className="font-mono">
            <Link to="/projects/new">
              <Plus data-icon="inline-start" aria-hidden="true" />
              New project
            </Link>
          </Button>
        }
      />

      <div className="flex flex-col gap-6 px-8 py-6">
        {error && <p className="text-sm text-destructive">{String(error)}</p>}
        {isPending && !error && (
          <p className="text-sm text-muted-foreground">Loading…</p>
        )}

        {data && instances.length === 0 && (
          <p className="text-sm text-muted-foreground">
            No trau loops are running on this machine right now.
          </p>
        )}

        {instances.length > 0 && (
          <div className="grid gap-4 sm:grid-cols-2">
            {instances.map((instance) => (
              <InstanceCard key={instance.pid} instance={instance} now={now} />
            ))}
          </div>
        )}

        {repos.error && (
          <p className="text-sm text-destructive">{String(repos.error)}</p>
        )}

        {repoViews.length > 0 && (
          <TerminalCard title="repos" bodyClassName="p-0">
            <ul className="flex flex-col divide-y divide-border/60">
              {rows.map((row) =>
                row.project ? (
                  <ProjectGroup
                    key={row.project.id}
                    project={row.project}
                    repos={row.repos}
                    now={now}
                  />
                ) : (
                  <RepoHealthRow
                    key={row.repos[0].root}
                    repo={row.repos[0]}
                    now={now}
                  />
                ),
              )}
            </ul>
          </TerminalCard>
        )}
      </div>
    </>
  )
}

// ProjectGroup renders the folder its members were registered under: a header over
// the same rows an ungrouped repo gets, with the one control that reaches the whole
// folder. groupRepos only forms a group at two or more members, so a lone repo and a
// single-member project both stay bare rows and never land here. Members sit inset
// behind a rail, so the row that ends a group reads as the group's rather than as
// the ungrouped row following it.
function ProjectGroup({
  project,
  repos,
  now,
}: {
  project: ProjectView
  repos: RepoView[]
  now: number
}) {
  return (
    <li className="flex flex-col">
      <div className="flex items-center gap-2 border-b border-border/60 bg-secondary/20 px-5 py-2.5">
        <FolderGit2
          className="size-3.5 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
        <span className="truncate font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground">
          {project.name}
        </span>
        <span className="shrink-0 font-mono text-[0.65rem] tabular-nums text-muted-foreground">
          {repos.length} repos
        </span>
        <span className="ml-auto">
          <RemoveProjectButton project={project} repos={repos} />
        </span>
      </div>
      <ul className="ml-5 flex flex-col divide-y divide-border/60 border-l border-border/60">
        {repos.map((repo) => (
          <RepoHealthRow key={repo.root} repo={repo} now={now} />
        ))}
      </ul>
    </li>
  )
}

function RepoHealthRow({ repo, now }: { repo: RepoView; now: number }) {
  const state = repoHealth(repo)
  const pill = healthPill(state)
  return (
    <li className="flex flex-col gap-3 px-5 py-4 lg:grid lg:grid-cols-[14rem_1fr_auto] lg:items-start lg:gap-6">
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="truncate font-mono text-sm text-foreground">
          {repo.name}
        </span>
        <span
          className="truncate font-mono text-[0.65rem] text-muted-foreground"
          title={repo.root}
        >
          {repo.root}
        </span>
      </div>
      <div className="flex min-w-0 flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill state={pill.state} label={pill.label} />
          {!repo.allowed && <Badge variant="secondary">observe-only</Badge>}
        </div>
        <RepoFreshnessLine repo={repo} state={state} now={now} />
      </div>
      <RepoHealthActions repo={repo} state={state} />
    </li>
  )
}

function RepoFreshnessLine({
  repo,
  state,
  now,
}: {
  repo: RepoView
  state: RepoHealthState
  now: number
}) {
  const freshness = repo.freshness
  const synced = freshness?.last_synced_at

  if (state === 'sync-failed' || state === 'degraded') {
    const failed = state === 'sync-failed'
    return (
      <div
        role="alert"
        className={cn(
          'rounded-md border px-3 py-2',
          failed ? 'border-fail/40 bg-fail/5' : 'border-warn/40 bg-warn/5',
        )}
      >
        <p
          className={cn(
            'break-words font-mono text-xs leading-relaxed',
            failed ? 'text-fail' : 'text-warn',
          )}
        >
          {freshness?.last_error}
        </p>
        {!failed && (
          <p className="mt-1 font-sans text-xs leading-relaxed text-muted-foreground">
            {freshness?.issue_count ?? 0} issues still served from the last good
            sync{synced && ` · ${formatElapsed(synced, now)} ago`}.
          </p>
        )}
      </div>
    )
  }

  if (state === 'unconfigured') {
    return (
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        Registered, but no tracker is configured — the backlog and runs stay
        empty until the setup wizard finishes.
      </p>
    )
  }

  if (state === 'never-synced') {
    return (
      <p className="font-sans text-xs leading-relaxed text-muted-foreground">
        Configured, but the issue store has never been seeded. Sync to pull the
        backlog in.
      </p>
    )
  }

  return (
    <p className="font-mono text-xs text-muted-foreground">
      {freshness?.issue_count ?? 0} issues
      {synced && ` · synced ${formatElapsed(synced, now)} ago`}
      {state === 'syncing' && <span className="text-teal"> · pulling now…</span>}
    </p>
  )
}

function RepoHealthActions({
  repo,
  state,
}: {
  repo: RepoView
  state: RepoHealthState
}) {
  const queryClient = useQueryClient()
  const sync = useMutation({
    mutationFn: () => syncRepo(repo.name),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['repos'] })
      void queryClient.invalidateQueries({ queryKey: ['backlog', repo.name] })
    },
  })

  const degradedConfig =
    state === 'degraded' && repo.freshness?.last_error_kind === 'config'
  const broken =
    state === 'unconfigured' || state === 'sync-failed' || degradedConfig
  const resyncable =
    state === 'sync-failed' || state === 'never-synced' || state === 'degraded'

  return (
    <div className="flex flex-col gap-1.5 lg:items-end lg:pt-0.5">
      <div className="flex flex-wrap items-center gap-2">
        {broken && (
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link to="/projects/new" search={{ path: repo.root }}>
              <Wrench data-icon="inline-start" aria-hidden="true" />
              Fix configuration
            </Link>
          </Button>
        )}
        {resyncable && (
          <Button
            variant="ghost"
            size="sm"
            className="font-mono"
            disabled={sync.isPending}
            onClick={() => sync.mutate()}
          >
            <RotateCw data-icon="inline-start" aria-hidden="true" />
            {syncLabel(state, sync.isPending)}
          </Button>
        )}
        {!repo.allowed && <MakeStartableButton root={repo.root} size="sm" />}
        {repo.registered && (
          <UnregisterRepoButton repo={repo.name} root={repo.root} />
        )}
        <RemoveRepoButton
          repo={repo.name}
          root={repo.root}
          blocked={removeBlocked(repo)}
        />
      </div>
      {sync.error && (
        <p className="font-mono text-xs text-fail">
          {(sync.error as Error).message}
        </p>
      )}
    </div>
  )
}

function syncLabel(state: RepoHealthState, pending: boolean): string {
  if (pending) return 'Syncing…'
  return state === 'never-synced' ? 'Sync now' : 'Retry sync'
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
