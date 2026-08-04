import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { ExternalLink, GitBranch, Loader2, Send, Video } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import {
  CriteriaChecklist,
  NoSkillsBanner,
  NoBrowserBanner,
  PRStatusBadge,
  RemovedBanner,
  RunPageHeader,
  SteerComposer,
  SteerNotesTimeline,
  StatusPill,
  TerminalCard,
  UnverifiedCriteriaBanner,
  useRepoRouteScope,
  type RunState,
} from '@/components/trau'
import { Markdown } from '@/components/markdown'
import { cn } from '@/lib/utils'
import { boardPill } from '@/lib/overview'
import { addComment } from '@/lib/issues'
import { runTitle, usePageTitle } from '@/lib/page-title'
import type { FailureClass } from '@/lib/runs'
import { formatCostUSD, formatDuration } from '@/lib/runlive'
import { steerSettled } from '@/lib/steer'
import {
  renderRunVideo,
  runDetailQueryOptions,
  runProofsQueryOptions,
  type Anomaly,
  type PhaseCost,
  type Proof,
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
  useRepoRouteScope(repo)
  const { data, error, isPending } = useQuery(runDetailQueryOptions(repo, ticket))

  usePageTitle(runTitle(ticket, data ? boardPill(data).label : ''))

  return (
    <div className="flex flex-col gap-4">
      {data ? (
        <Detail repo={repo} run={data} />
      ) : (
        <>
          <RunPageHeader ticket={ticket} />
          {error && (
            <p className="font-mono text-sm text-destructive">{String(error)}</p>
          )}
          {isPending && !error && (
            <p className="font-mono text-sm text-muted-foreground">Loading…</p>
          )}
        </>
      )}
    </div>
  )
}

function Detail({ repo, run }: { repo: string; run: RunDetail }) {
  const pill = boardPill(run)
  const tone = failureTone(run.failure_class)
  const openPR = run.pr_url ? (
    <Button asChild size="sm" className="font-mono">
      <a href={run.pr_url} target="_blank" rel="noreferrer">
        <ExternalLink className="size-3.5" aria-hidden="true" />
        {run.pr ? `PR #${run.pr}` : 'Open PR'}
      </a>
    </Button>
  ) : null

  return (
    <div className="flex flex-col gap-4">
      <RunPageHeader
        ticket={run.ticket}
        title={run.title}
        badges={<StatusPill state={pill.state} label={pill.label} />}
        actions={
          <>
            {openPR}
            <PRStatusBadge status={run.pr_status} />
          </>
        }
        meta={
          <div className="flex flex-wrap items-center gap-2 font-mono text-[0.7rem] text-muted-foreground">
            <span className="rounded border border-border bg-muted/60 px-1.5 py-0.5 text-[0.65rem]">
              {repo}
            </span>
            {run.branch && (
              <span className="inline-flex items-center gap-1.5">
                <GitBranch className="size-3.5" aria-hidden="true" />
                {run.branch}
              </span>
            )}
            {run.updated_at && (
              <span className="tabular-nums">updated {run.updated_at}</span>
            )}
          </div>
        }
      />

      {run.removed && <RemovedBanner />}

      {run.no_skills && <NoSkillsBanner />}

      {run.no_browser && <NoBrowserBanner />}

      {run.unverified_criteria && <UnverifiedCriteriaBanner />}

      {run.failure_reason && (
        <div className={cn('rounded-lg border px-4 py-3', tone.box)}>
          <p className={cn('font-mono text-sm leading-relaxed', tone.text)}>
            {run.failure_reason}
          </p>
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <TerminalCard title="steer notes" className="lg:col-span-2">
          <div className="flex flex-col gap-5">
            <SteerComposer
              repo={repo}
              ticket={run.ticket}
              settled={steerSettled(run)}
              showQueued={false}
            />
            <SteerNotesTimeline repo={repo} ticket={run.ticket} />
          </div>
        </TerminalCard>

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

        <ProofsCard repo={repo} ticket={run.ticket} />

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

// A blameless park keeps the destructive red off the screen: a deliberate stop
// reads informational, a spend ceiling reads as a warning like its board pill.
function failureTone(failureClass?: FailureClass): { box: string; text: string } {
  switch (failureClass) {
    case 'stopped':
      return { box: 'border-info/40 bg-info/10', text: 'text-info' }
    case 'budget':
      return { box: 'border-warn/40 bg-warn/10', text: 'text-warn' }
    default:
      return { box: 'border-fail/40 bg-fail/10', text: 'text-fail' }
  }
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
      {verdict.criteria && verdict.criteria.length > 0 && (
        <CriteriaChecklist criteria={verdict.criteria} />
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
      {verdict.browser && (
        <div className="flex flex-col gap-1.5 border-t border-border/60 pt-3">
          <div className="flex items-center gap-2 font-mono text-[0.65rem] uppercase tracking-[0.18em] text-muted-foreground">
            browser QA
            <StatusPill state={browserPill(verdict.browser)} label={verdict.browser} />
          </div>
          {verdict.browser_notes && (
            <p className="font-sans text-sm leading-relaxed text-muted-foreground">
              {verdict.browser_notes}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function browserPill(outcome: string): RunState {
  switch (outcome) {
    case 'driven':
      return 'success'
    case 'skipped':
      return 'warn'
    default:
      return 'todo'
  }
}

function ProofsCard({ repo, ticket }: { repo: string; ticket: string }) {
  const { data } = useQuery(runProofsQueryOptions(repo, ticket))
  const [active, setActive] = useState<Proof | null>(null)
  const proofs = data ?? []
  const shots = proofs.filter((p) => p.is_image && p.url)
  const video = proofs.find((p) => p.kind === 'video')

  if (shots.length === 0 && !video) {
    return null
  }
  return (
    <TerminalCard title="QA proofs" className="lg:col-span-2">
      <div className="flex flex-col gap-4">
        {shots.length > 0 && (
          <ul className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {shots.map((p) => (
              <li key={p.seq}>
                <button
                  type="button"
                  onClick={() => setActive(p)}
                  className="group flex w-full flex-col gap-1.5 text-left"
                >
                  <img
                    src={p.url}
                    alt={p.caption || `proof ${p.seq}`}
                    loading="lazy"
                    className="aspect-video w-full rounded-md border border-border object-cover transition-colors group-hover:border-primary"
                  />
                  {p.caption && (
                    <span className="truncate font-mono text-[0.7rem] text-muted-foreground">
                      {p.caption}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
        <VideoProof repo={repo} ticket={ticket} video={video} />
      </div>
      <Lightbox proof={active} onClose={() => setActive(null)} />
    </TerminalCard>
  )
}

function VideoProof({
  repo,
  ticket,
  video,
}: {
  repo: string
  ticket: string
  video?: Proof
}) {
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: () => renderRunVideo(repo, ticket),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: ['run-proofs', repo, ticket],
      }),
  })

  const rendering = mutation.isPending || video?.render === 'rendering'
  const hasVideo = Boolean(video?.url)
  const disabledReason = video?.trace_exists
    ? ''
    : video?.trace_dir
      ? 'trace pruned'
      : 'no recording captured'

  return (
    <div className="flex flex-col gap-2">
      {hasVideo && video && (
        <figure className="flex flex-col gap-1.5">
          <video
            src={video.url}
            controls
            className="w-full rounded-md border border-border"
          />
          {video.caption && (
            <figcaption className="font-mono text-[0.7rem] text-muted-foreground">
              {video.caption}
            </figcaption>
          )}
        </figure>
      )}
      <div className="flex items-center gap-3">
        <Button
          size="sm"
          variant={hasVideo ? 'outline' : 'default'}
          className="font-mono"
          disabled={rendering || !video?.trace_exists}
          onClick={() => mutation.mutate()}
        >
          {rendering ? (
            <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <Video className="size-3.5" aria-hidden="true" />
          )}
          {rendering
            ? 'Rendering…'
            : hasVideo
              ? 'Re-render video'
              : 'Render video'}
        </Button>
        {!rendering && disabledReason && (
          <span className="font-mono text-xs text-muted-foreground">
            {disabledReason}
          </span>
        )}
        {rendering && (
          <span className="font-mono text-xs text-muted-foreground">
            vision-heavy agent session; this can take a few minutes
          </span>
        )}
      </div>
      {!rendering && video?.render === 'failed' && video.render_error && (
        <span className="font-mono text-xs text-destructive">
          {video.render_error}
        </span>
      )}
      {mutation.error && (
        <span className="font-mono text-xs text-destructive">
          {(mutation.error as Error).message}
        </span>
      )}
    </div>
  )
}

function Lightbox({ proof, onClose }: { proof: Proof | null; onClose: () => void }) {
  return (
    <Dialog open={proof !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-4xl gap-2 border-border p-2">
        {proof && (
          <>
            <DialogTitle className="sr-only">
              {proof.caption || `Proof ${proof.seq}`}
            </DialogTitle>
            <img
              src={proof.url}
              alt={proof.caption || `proof ${proof.seq}`}
              className="max-h-[80vh] w-full rounded object-contain"
            />
            {proof.caption && (
              <p className="px-2 pb-1 text-center font-mono text-xs text-muted-foreground">
                {proof.caption}
              </p>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
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
      <Textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={`Add a comment to ${ticket}…`}
        rows={3}
        className="resize-y font-sans"
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
