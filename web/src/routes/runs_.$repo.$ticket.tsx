import { useEffect, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, createFileRoute } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink, GitBranch, Send } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Eyebrow,
  NoSkillsBanner,
  NoticeBanner,
  RemovedBanner,
  RunActionsRow,
  StatusPill,
  TerminalCard,
  useActiveRepo,
  type CheckpointNotice,
} from '@/components/trau'
import { Markdown } from '@/components/markdown'
import { cn } from '@/lib/utils'
import { boardPill } from '@/lib/board'
import { addComment } from '@/lib/issues'
import { formatCostUSD, formatDuration } from '@/lib/runlive'
import {
  runDetailQueryOptions,
  type Anomaly,
  type PhaseCost,
  type RunDetail,
  type Rubric,
  type StepDuration,
  type Verdict,
} from '@/lib/rundetail'

export const Route = createFileRoute('/runs_/$repo/$ticket')({
  component: RunDetailPage,
})

function RunDetailPage() {
  const { repo, ticket } = Route.useParams()
  const { setRepo } = useActiveRepo()
  const { data, error, isPending } = useQuery(runDetailQueryOptions(repo, ticket))
  const [notice, setNotice] = useState<CheckpointNotice | null>(null)

  useEffect(() => {
    setRepo(repo)
  }, [repo, setRepo])

  return (
    <div className="flex flex-col gap-6">
      <Link
        to="/runs"
        className="inline-flex w-fit items-center gap-1.5 font-mono text-sm text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="size-4" aria-hidden="true" />
        Runs
      </Link>

      {error && <p className="font-mono text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="font-mono text-sm text-muted-foreground">Loading…</p>
      )}
      {data && (
        <Detail
          repo={repo}
          run={data}
          notice={notice}
          onNotice={setNotice}
          onDismiss={() => setNotice(null)}
        />
      )}
    </div>
  )
}

function Detail({
  repo,
  run,
  notice,
  onNotice,
  onDismiss,
}: {
  repo: string
  run: RunDetail
  notice: CheckpointNotice | null
  onNotice: (notice: CheckpointNotice) => void
  onDismiss: () => void
}) {
  const pill = boardPill(run)
  const openPR = run.pr_url ? (
    <Button asChild size="sm" className="font-mono">
      <a href={run.pr_url} target="_blank" rel="noreferrer">
        <ExternalLink className="size-3.5" aria-hidden="true" />
        {run.pr ? `PR #${run.pr}` : 'Open PR'}
      </a>
    </Button>
  ) : null

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-col gap-3">
        <Eyebrow glyph="partial" className="text-info">
          RUN DETAIL
        </Eyebrow>
        <div className="flex flex-wrap items-center gap-3">
          <span className="font-mono text-sm text-primary">{run.ticket}</span>
          {run.title && (
            <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
              {run.title}
            </h1>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2 font-mono text-[0.7rem] text-muted-foreground">
          <StatusPill state={pill.state} label={pill.label} />
          <span className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[0.65rem]">
            {repo}
          </span>
          {run.branch && (
            <span className="inline-flex items-center gap-1.5">
              <GitBranch className="size-3.5" aria-hidden="true" />
              {run.branch}
            </span>
          )}
          {run.updated_at && <span className="tabular-nums">updated {run.updated_at}</span>}
        </div>
      </header>

      <RunActionsRow
        repo={repo}
        ticket={run.ticket}
        phase={run.phase}
        onNotice={onNotice}
        leading={openPR}
      />

      {notice && <NoticeBanner notice={notice} onDismiss={onDismiss} />}

      {run.removed && <RemovedBanner />}

      {run.no_skills && <NoSkillsBanner />}

      {run.failure_reason && (
        <div className="rounded-lg border border-fail/40 bg-fail/10 px-4 py-3">
          <p className="font-mono text-sm leading-relaxed text-fail">{run.failure_reason}</p>
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <TerminalCard title="handoff.md" className="lg:col-span-2">
          {run.artifacts.handoff && run.handoff ? (
            <Markdown>{run.handoff}</Markdown>
          ) : (
            <Empty>No handoff brief yet — this run has not reached the handoff phase.</Empty>
          )}
        </TerminalCard>

        <TerminalCard title="rubric">
          <RubricView rubric={run.rubric} present={run.artifacts.rubric} />
        </TerminalCard>

        <TerminalCard title="verify" scanlines>
          <VerdictView verdict={run.verdict} present={run.artifacts.verdict} />
        </TerminalCard>

        <TerminalCard title="costs">
          <CostsView
            costs={run.costs}
            durations={run.durations}
            present={run.artifacts.tokens}
          />
        </TerminalCard>

        <TerminalCard title="anomalies">
          <AnomaliesView anomalies={run.anomalies} />
        </TerminalCard>

        <TerminalCard title="buildnotes.md" className="lg:col-span-2">
          {run.artifacts.build_notes && run.build_notes ? (
            <Markdown>{run.build_notes}</Markdown>
          ) : (
            <Empty>No build notes for this run — the build agent left none.</Empty>
          )}
        </TerminalCard>

        <TerminalCard title="comments" className="lg:col-span-2">
          <CommentComposer repo={repo} ticket={run.ticket} />
        </TerminalCard>
      </div>
    </div>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return <p className="font-sans text-sm leading-relaxed text-muted-foreground">{children}</p>
}

function RubricView({ rubric, present }: { rubric?: Rubric; present: boolean }) {
  const groups: { label: string; items?: string[] }[] = [
    { label: 'Acceptance criteria', items: rubric?.acceptance_criteria },
    { label: 'Required tests', items: rubric?.required_tests },
    { label: 'UI paths', items: rubric?.ui_paths },
    { label: 'Non-goals', items: rubric?.non_goals },
    { label: 'Fail conditions', items: rubric?.fail_conditions },
  ].filter((g) => g.items && g.items.length > 0)

  if (!present || groups.length === 0) {
    return <Empty>No rubric distilled for this run yet.</Empty>
  }
  return (
    <div className="flex flex-col gap-4">
      {groups.map((g) => (
        <div key={g.label} className="flex flex-col gap-1.5">
          <p className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            {g.label}
          </p>
          <ul className="flex flex-col gap-1.5">
            {g.items!.map((item, i) => (
              <li key={i} className="flex items-start gap-2.5">
                <span aria-hidden="true" className="mt-0.5 font-mono text-sm text-faint">
                  ›
                </span>
                <span className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
                  {item}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  )
}

function VerdictView({ verdict, present }: { verdict?: Verdict; present: boolean }) {
  if (!present || !verdict) {
    return <Empty>Not verified yet — no verdict recorded for this run.</Empty>
  }
  return (
    <div className="flex flex-col gap-3">
      <StatusPill
        state={verdict.pass ? 'success' : 'fail'}
        label={verdict.pass ? 'PASS' : 'FAIL'}
      />
      {verdict.summary && (
        <p className="font-sans text-sm leading-relaxed text-muted-foreground">
          {verdict.summary}
        </p>
      )}
      {verdict.failures && verdict.failures.length > 0 && (
        <ul className="flex flex-col gap-1 border-l-2 border-fail/40 pl-3">
          {verdict.failures.map((f, i) => (
            <li key={i} className="font-sans text-sm text-fail">
              {f}
            </li>
          ))}
        </ul>
      )}
      {verdict.checks && verdict.checks.length > 0 && (
        <ul className="flex flex-col gap-1.5 border-t border-border/60 pt-3 font-mono text-sm">
          {verdict.checks.map((c) => (
            <li key={c.name} className="flex items-start gap-2.5">
              <span
                aria-hidden="true"
                className={cn('mt-0.5', c.pass ? 'text-done' : 'text-fail')}
              >
                {c.pass ? '✓' : '✗'}
              </span>
              <span className="text-foreground">{c.name}</span>
              {c.detail && <span className="text-muted-foreground">— {c.detail}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function CostsView({
  costs,
  durations,
  present,
}: {
  costs: PhaseCost[]
  durations?: StepDuration[]
  present: boolean
}) {
  if (!present || costs.length === 0) {
    return <Empty>No token spend recorded for this run yet.</Empty>
  }
  const totalUsd = costs.reduce((acc, c) => acc + c.cost_usd, 0)
  const metered = costs.every((c) => c.metered)
  return (
    <div className="flex flex-col gap-4">
      <table className="w-full border-collapse font-mono text-sm">
        <tbody>
          {costs.map((c) => (
            <tr key={c.phase} className="border-b border-border/60">
              <td className="py-2 text-muted-foreground">{c.phase}</td>
              <td className="py-2 text-right tabular-nums text-foreground">
                {formatCostUSD(c.cost_usd, c.metered)}
              </td>
            </tr>
          ))}
          <tr>
            <td className="py-2 text-foreground">total</td>
            <td className="py-2 text-right tabular-nums text-primary">
              {formatCostUSD(totalUsd, metered)}
            </td>
          </tr>
        </tbody>
      </table>
      <StepDurations durations={durations} />
    </div>
  )
}

// StepDurations renders the run's wall-clock grouped by Step (Build/Verify/Ship),
// derived hub-side from activity_change deltas — CI wait folded into Ship, visible
// for the first time. It renders nothing for a run predating the signal, so an old
// run shows no durations rather than a guess.
function StepDurations({ durations }: { durations?: StepDuration[] }) {
  if (!durations || durations.length === 0) {
    return null
  }
  return (
    <div className="flex flex-col gap-2">
      <p className="font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
        duration by step
      </p>
      <table className="w-full border-collapse font-mono text-sm">
        <tbody>
          {durations.map((d) => (
            <tr key={d.step} className="border-b border-border/60">
              <td className="py-2 text-muted-foreground">{d.step}</td>
              <td className="py-2 text-right tabular-nums text-foreground">
                {formatDuration(d.duration_ms)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function AnomaliesView({ anomalies }: { anomalies?: Anomaly[] }) {
  if (!anomalies || anomalies.length === 0) {
    return <Empty>No cost anomalies flagged for this run.</Empty>
  }
  return (
    <ul className="flex flex-col gap-2.5">
      {anomalies.map((a) => (
        <li key={a.phase} className="flex items-start gap-2.5">
          <span aria-hidden="true" className="mt-0.5 font-mono text-sm text-warn">
            ⚠
          </span>
          <div className="flex flex-col gap-0.5">
            <span className="text-pretty font-sans text-sm leading-relaxed text-foreground">
              {a.phase} — {a.reasons.join(', ')}
            </span>
            <span className="font-mono text-[0.7rem] tabular-nums text-muted-foreground">
              {formatCostUSD(a.cost_usd, true)} · {a.output.toLocaleString()} output · {a.turns}{' '}
              turns
            </span>
          </div>
        </li>
      ))}
    </ul>
  )
}

function CommentComposer({ repo, ticket }: { repo: string; ticket: string }) {
  const [body, setBody] = useState('')
  const mutation = useMutation({
    mutationFn: (text: string) => addComment(repo, ticket, text),
    onSuccess: () => setBody(''),
  })

  return (
    <div className="flex flex-col gap-3">
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={`Add a comment to ${ticket}…`}
        rows={3}
        className="w-full resize-y rounded-md border border-border bg-input px-3 py-2 font-sans text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring"
      />
      <div className="flex items-center gap-3">
        <Button
          size="sm"
          className="font-mono"
          disabled={mutation.isPending || body.trim() === ''}
          onClick={() => mutation.mutate(body.trim())}
        >
          <Send className="size-3.5" aria-hidden="true" />
          {mutation.isPending ? 'Posting…' : 'Post comment'}
        </Button>
        {mutation.isSuccess && (
          <span className="font-mono text-xs text-done">Comment posted.</span>
        )}
        {mutation.error && (
          <span className="font-mono text-xs text-destructive">
            {(mutation.error as Error).message}
          </span>
        )}
      </div>
    </div>
  )
}
