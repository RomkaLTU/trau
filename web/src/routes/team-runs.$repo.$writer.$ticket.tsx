import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { ExternalLink, GitBranch, Users } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  RunPageHeader,
  StatusPill,
  TerminalCard,
  useRepoRouteScope,
} from '@/components/trau'
import { boardPill } from '@/lib/overview'
import { runTitle, usePageTitle } from '@/lib/page-title'
import { formatCostUSD, formatDuration } from '@/lib/runlive'
import { teamRunsQueryOptions, type Run } from '@/lib/runs'

export const Route = createFileRoute('/team-runs/$repo/$writer/$ticket')({
  component: TeamRunPage,
})

// TeamRunPage renders one teammate's run from the record that actually crossed the
// team-sync remote — the summary, the Activity timeline, and the spend. There are
// deliberately no transcript, log, or artifact sections: none of that travels, and
// an empty tab would suggest it might arrive later.
function TeamRunPage() {
  const { repo, writer, ticket } = Route.useParams()
  useRepoRouteScope(repo)
  const { data, error, isPending } = useQuery(teamRunsQueryOptions(repo))
  const run = data?.runs.find((r) => r.ticket === ticket && r.shared?.writer === writer)

  usePageTitle(runTitle(ticket, run ? boardPill(run).label : ''))

  return (
    <div className="flex flex-col gap-4">
      {run ? (
        <TeamRunDetail repo={repo} run={run} />
      ) : (
        <>
          <RunPageHeader ticket={ticket} />
          {error && (
            <p className="font-mono text-sm text-destructive">{String(error)}</p>
          )}
          {isPending && !error && (
            <p className="font-mono text-sm text-muted-foreground">Loading…</p>
          )}
          {data && (
            <p className="font-mono text-sm text-muted-foreground">
              This teammate’s run is no longer in the shared history for {repo}.
            </p>
          )}
        </>
      )}
    </div>
  )
}

function TeamRunDetail({ repo, run }: { repo: string; run: Run }) {
  const shared = run.shared!
  const pill = boardPill(run)
  const author = shared.author ?? shared.writer

  return (
    <div className="flex flex-col gap-6">
      <RunPageHeader
        ticket={run.ticket}
        title={run.title}
        meta={
          <div className="flex flex-wrap items-center gap-2 font-mono text-[0.7rem] text-muted-foreground">
            <StatusPill state={pill.state} label={pill.label} />
            <span className="inline-flex items-center gap-1.5 rounded-full border border-info/40 bg-info/10 px-2 py-0.5 text-[0.65rem] text-info">
              <Users className="size-3" aria-hidden="true" />
              {author}
            </span>
            <span className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[0.65rem]">
              {shared.repo ?? repo}
            </span>
            {run.branch && (
              <span className="inline-flex items-center gap-1.5">
                <GitBranch className="size-3.5" aria-hidden="true" />
                {run.branch}
              </span>
            )}
          </div>
        }
      />

      <div className="rounded-lg border border-info/40 bg-info/10 px-4 py-3">
        <p className="font-sans text-sm leading-relaxed text-info">
          {author} ran this on their own machine. Only this summary travelled — the
          transcript, phase logs, and event stream stayed there.
        </p>
      </div>

      {run.pr_url && (
        <Button asChild size="sm" className="w-fit font-mono">
          <a href={run.pr_url} target="_blank" rel="noreferrer">
            <ExternalLink className="size-3.5" aria-hidden="true" />
            Open PR
          </a>
        </Button>
      )}

      {run.failure_reason && (
        <div className="rounded-lg border border-fail/40 bg-fail/10 px-4 py-3">
          <p className="font-mono text-sm leading-relaxed text-fail">{run.failure_reason}</p>
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <TerminalCard title="summary">
          <dl className="flex flex-col font-mono text-sm">
            <Field label="outcome" value={pill.label} />
            <Field label="started" value={shared.started_at} />
            <Field label="ended" value={shared.ended_at} />
            <Field label="fetched" value={shared.fetched_at} />
          </dl>
        </TerminalCard>

        <TerminalCard title="cost">
          <div className="flex flex-col gap-3">
            <p className="font-mono text-2xl tabular-nums text-primary">
              {run.cost_usd === undefined ? '—' : formatCostUSD(run.cost_usd, true)}
            </p>
            <dl className="flex flex-col font-mono text-sm">
              <Field label="provider" value={shared.provider} />
              <Field label="model" value={shared.model} />
              <Field label="tokens" value={shared.tokens?.toLocaleString()} />
            </dl>
          </div>
        </TerminalCard>

        <TerminalCard title="activity timeline" className="lg:col-span-2">
          <ActivityTimeline run={run} />
        </TerminalCard>
      </div>
    </div>
  )
}

function Field({ label, value }: { label: string; value?: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-border/60 py-1.5">
      <dt className="text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        {label}
      </dt>
      <dd className="truncate tabular-nums text-foreground">{value || '—'}</dd>
    </div>
  )
}

function ActivityTimeline({ run }: { run: Run }) {
  const spans = run.shared?.activities ?? []
  if (spans.length === 0) {
    return (
      <p className="font-sans text-sm leading-relaxed text-muted-foreground">
        No timeline travelled with this run — it predates the activity signal.
      </p>
    )
  }
  const longest = Math.max(...spans.map((s) => s.duration_ms))
  return (
    <ul className="flex flex-col gap-2">
      {spans.map((span) => (
        <li key={span.activity} className="flex items-center gap-3 font-mono text-sm">
          <span className="w-24 shrink-0 text-muted-foreground">{span.activity}</span>
          <span className="h-2 flex-1 overflow-hidden rounded-full bg-muted/60">
            <span
              className="block h-full rounded-full bg-info"
              style={{ width: `${(span.duration_ms / longest) * 100}%` }}
            />
          </span>
          <span className="w-20 shrink-0 text-right tabular-nums text-foreground">
            {formatDuration(span.duration_ms)}
          </span>
        </li>
      ))}
    </ul>
  )
}
