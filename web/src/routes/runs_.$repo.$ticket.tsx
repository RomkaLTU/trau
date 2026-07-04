import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, createFileRoute } from '@tanstack/react-router'
import {
  ArrowLeft,
  CircleCheck,
  CircleX,
  FileText,
  Flame,
  GitBranch,
  GitPullRequest,
  ListChecks,
  MessageSquarePlus,
  Receipt,
  RotateCcw,
  Send,
  TicketPlus,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { CheckpointControls } from '@/components/checkpoint-controls'
import { Markdown } from '@/components/markdown'
import { NewIssueForm, type IssueDefaults } from '@/components/new-issue-form'
import { cn } from '@/lib/utils'
import { addComment } from '@/lib/issues'
import { reposQueryOptions } from '@/lib/runs'
import {
  runDetailQueryOptions,
  type Anomaly,
  type PhaseCost,
  type RunDetail,
  type Rubric,
  type Verdict,
} from '@/lib/rundetail'

export const Route = createFileRoute('/runs_/$repo/$ticket')({
  component: RunDetailPage,
})

function RunDetailPage() {
  const { repo, ticket } = Route.useParams()
  const { data, error, isPending } = useQuery(
    runDetailQueryOptions(repo, ticket),
  )

  return (
    <div className="flex flex-col gap-6">
      <Link
        to="/runs"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        Runs
      </Link>

      {error && <p className="text-sm text-destructive">{String(error)}</p>}
      {isPending && !error && (
        <p className="text-sm text-muted-foreground">Loading…</p>
      )}
      {data && <Detail repo={repo} run={data} />}
    </div>
  )
}

function Detail({ repo, run }: { repo: string; run: RunDetail }) {
  return (
    <div className="flex flex-col gap-8">
      <Header repo={repo} run={run} />
      <Anomalies anomalies={run.anomalies} />
      <Costs costs={run.costs} present={run.artifacts.tokens} />
      <Handoff markdown={run.handoff} present={run.artifacts.handoff} />
      <RubricPanel rubric={run.rubric} present={run.artifacts.rubric} />
      <VerdictPanel verdict={run.verdict} present={run.artifacts.verdict} />
      <CheckpointSection repo={repo} run={run} />
      <CommentSection repo={repo} ticket={run.ticket} />
      <FollowUpSection repo={repo} run={run} />
    </div>
  )
}

function CheckpointSection({ repo, run }: { repo: string; run: RunDetail }) {
  const { data } = useQuery(reposQueryOptions)
  const live = data?.repos.find((r) => r.name === repo)?.live ?? false

  return (
    <Section icon={RotateCcw} title="Checkpoint">
      <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
        <p className="text-sm text-muted-foreground">
          Reset drops the branch and re-queues {run.ticket} on the tracker. Clear
          forgets the local checkpoint only — for a ticket finished out-of-band.
        </p>
        <CheckpointControls
          repo={repo}
          ticket={run.ticket}
          phase={run.phase}
          live={live}
        />
      </div>
    </Section>
  )
}

function CommentSection({ repo, ticket }: { repo: string; ticket: string }) {
  const [body, setBody] = useState('')
  const mutation = useMutation({
    mutationFn: (text: string) => addComment(repo, ticket, text),
    onSuccess: () => setBody(''),
  })

  return (
    <Section icon={MessageSquarePlus} title="Comment on this ticket">
      <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder={`Add a comment to ${ticket}…`}
          rows={3}
          className="w-full resize-y rounded-md border bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
        />
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => body.trim() !== '' && mutation.mutate(body.trim())}
            disabled={mutation.isPending || body.trim() === ''}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            <Send className="size-4" />
            {mutation.isPending ? 'Posting…' : 'Post comment'}
          </button>
          {mutation.isSuccess && (
            <span className="text-xs text-emerald-600 dark:text-emerald-400">
              Comment posted.
            </span>
          )}
          {mutation.error && (
            <span className="text-xs text-destructive">
              {String((mutation.error as Error).message)}
            </span>
          )}
        </div>
      </div>
    </Section>
  )
}

// followUpDefaults pre-fills a new-issue form with the run's context so a
// follow-up filed from a quarantined or faulted run carries the ticket it came
// from and why it stopped.
function followUpDefaults(repo: string, run: RunDetail): IssueDefaults {
  const lines = [
    `Filed from the trau hub while reviewing **${run.ticket}**${
      run.title ? ` — ${run.title}` : ''
    }.`,
    '',
    `- Repo: \`${repo}\``,
    `- Phase: ${run.phase === '' ? 'queued' : run.phase.replace(/_/g, ' ')}`,
  ]
  if (run.failure_reason) {
    lines.push(`- Failure reason: ${run.failure_reason}`)
  }
  return {
    title: `Follow-up: ${run.ticket}`,
    description: lines.join('\n'),
  }
}

function FollowUpSection({ repo, run }: { repo: string; run: RunDetail }) {
  return (
    <Section icon={TicketPlus} title="File a follow-up issue">
      <NewIssueForm repo={repo} defaults={followUpDefaults(repo, run)} />
    </Section>
  )
}

function Header({ repo, run }: { repo: string; run: RunDetail }) {
  return (
    <header className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-mono text-xl font-semibold">{run.ticket}</h1>
        <Badge variant="outline" className="capitalize">
          {run.phase === '' ? 'queued' : run.phase.replace(/_/g, ' ')}
        </Badge>
        {run.failure_class && (
          <Badge
            variant="outline"
            className={cn(
              run.failure_class === 'paused'
                ? 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400'
                : 'border-destructive/40 bg-destructive/10 text-destructive',
            )}
          >
            {run.failure_class === 'gave_up'
              ? 'quarantined'
              : run.failure_class}
          </Badge>
        )}
      </div>

      {run.title && (
        <p className="text-sm text-muted-foreground">{run.title}</p>
      )}
      {run.failure_reason && (
        <p className="text-sm text-destructive">{run.failure_reason}</p>
      )}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span className="text-foreground/70">{repo}</span>
        {run.branch && (
          <span className="inline-flex items-center gap-1.5 font-mono">
            <GitBranch className="size-3.5" />
            {run.branch}
          </span>
        )}
        {run.pr_url && (
          <a
            href={run.pr_url}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 text-foreground hover:underline"
          >
            <GitPullRequest className="size-3.5" />
            {run.pr ? `PR #${run.pr}` : 'PR'}
          </a>
        )}
        {run.updated_at && (
          <span className="tabular-nums">Updated {run.updated_at}</span>
        )}
      </div>
    </header>
  )
}

function num(n: number): string {
  return n.toLocaleString()
}

function cost(usd: number, metered: boolean): string {
  const formatted = `$${usd.toFixed(2)}`
  return metered ? formatted : `≥ ${formatted}`
}

function Section({
  icon: Icon,
  title,
  children,
}: {
  icon: typeof FileText
  title: string
  children: React.ReactNode
}) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="flex items-center gap-2 text-sm font-semibold text-foreground">
        <Icon className="size-4 text-muted-foreground" />
        {title}
      </h2>
      {children}
    </section>
  )
}

function Anomalies({ anomalies }: { anomalies?: Anomaly[] }) {
  if (!anomalies || anomalies.length === 0) return null
  return (
    <Section icon={Flame} title="Cost anomalies">
      <div className="flex flex-col gap-2">
        {anomalies.map((a) => (
          <div
            key={a.phase}
            className="flex flex-col gap-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3"
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-xs font-medium">{a.phase}</span>
              <span className="text-xs tabular-nums text-muted-foreground">
                {cost(a.cost_usd, true)} · {num(a.output)} output · {a.turns}{' '}
                turns
              </span>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {a.reasons.map((r, i) => (
                <Badge
                  key={i}
                  variant="outline"
                  className="border-amber-500/40 text-amber-700 dark:text-amber-300"
                >
                  {r}
                </Badge>
              ))}
            </div>
          </div>
        ))}
      </div>
    </Section>
  )
}

function Costs({ costs, present }: { costs: PhaseCost[]; present: boolean }) {
  return (
    <Section icon={Receipt} title="Per-phase cost">
      {!present || costs.length === 0 ? (
        <Empty>No token spend recorded for this run yet.</Empty>
      ) : (
        <CostTable costs={costs} />
      )}
    </Section>
  )
}

function CostTable({ costs }: { costs: PhaseCost[] }) {
  const totals = costs.reduce(
    (acc, c) => ({
      input: acc.input + c.input,
      output: acc.output + c.output,
      cache: acc.cache + c.cache_read + c.cache_creation,
      total: acc.total + c.total,
      usd: acc.usd + c.cost_usd,
      metered: acc.metered && c.metered,
    }),
    { input: 0, output: 0, cache: 0, total: 0, usd: 0, metered: true },
  )

  return (
    <div className="overflow-x-auto rounded-lg border">
      <table className="w-full min-w-max text-sm">
        <thead>
          <tr className="border-b bg-muted/40 text-left text-xs text-muted-foreground">
            <th className="px-3 py-2 font-medium">Phase</th>
            <th className="px-3 py-2 text-right font-medium">Input</th>
            <th className="px-3 py-2 text-right font-medium">Output</th>
            <th className="px-3 py-2 text-right font-medium">Cache</th>
            <th className="px-3 py-2 text-right font-medium">Total</th>
            <th className="px-3 py-2 text-right font-medium">Cost</th>
          </tr>
        </thead>
        <tbody>
          {costs.map((c) => (
            <tr key={c.phase} className="border-b last:border-b-0">
              <td className="px-3 py-2 font-mono text-xs">{c.phase}</td>
              <td className="px-3 py-2 text-right tabular-nums">
                {num(c.input)}
              </td>
              <td className="px-3 py-2 text-right tabular-nums">
                {num(c.output)}
              </td>
              <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
                {num(c.cache_read + c.cache_creation)}
              </td>
              <td className="px-3 py-2 text-right font-medium tabular-nums">
                {num(c.total)}
              </td>
              <td className="px-3 py-2 text-right tabular-nums">
                {cost(c.cost_usd, c.metered)}
              </td>
            </tr>
          ))}
        </tbody>
        <tfoot>
          <tr className="bg-muted/40 font-medium">
            <td className="px-3 py-2 text-xs">Total</td>
            <td className="px-3 py-2 text-right tabular-nums">
              {num(totals.input)}
            </td>
            <td className="px-3 py-2 text-right tabular-nums">
              {num(totals.output)}
            </td>
            <td className="px-3 py-2 text-right tabular-nums text-muted-foreground">
              {num(totals.cache)}
            </td>
            <td className="px-3 py-2 text-right tabular-nums">
              {num(totals.total)}
            </td>
            <td className="px-3 py-2 text-right tabular-nums">
              {cost(totals.usd, totals.metered)}
            </td>
          </tr>
        </tfoot>
      </table>
    </div>
  )
}

function Handoff({
  markdown,
  present,
}: {
  markdown?: string
  present: boolean
}) {
  return (
    <Section icon={FileText} title="Handoff brief">
      {!present || !markdown ? (
        <Empty>
          No handoff brief yet — this run has not reached the handoff phase.
        </Empty>
      ) : (
        <div className="rounded-lg border bg-card p-4">
          <Markdown>{markdown}</Markdown>
        </div>
      )}
    </Section>
  )
}

function RubricPanel({
  rubric,
  present,
}: {
  rubric?: Rubric
  present: boolean
}) {
  const groups: { label: string; items?: string[] }[] = [
    { label: 'Acceptance criteria', items: rubric?.acceptance_criteria },
    { label: 'Required tests', items: rubric?.required_tests },
    { label: 'UI paths', items: rubric?.ui_paths },
    { label: 'Non-goals', items: rubric?.non_goals },
    { label: 'Fail conditions', items: rubric?.fail_conditions },
  ].filter((g) => g.items && g.items.length > 0)

  return (
    <Section icon={ListChecks} title="Acceptance rubric">
      {!present || groups.length === 0 ? (
        <Empty>No rubric distilled for this run yet.</Empty>
      ) : (
        <div className="flex flex-col gap-4 rounded-lg border bg-card p-4">
          {groups.map((g) => (
            <div key={g.label} className="flex flex-col gap-1.5">
              <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {g.label}
              </p>
              <ul className="flex flex-col gap-1">
                {g.items!.map((item, i) => (
                  <li key={i} className="flex gap-2 text-sm">
                    <span className="mt-2 size-1 shrink-0 rounded-full bg-muted-foreground/50" />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </Section>
  )
}

function VerdictPanel({
  verdict,
  present,
}: {
  verdict?: Verdict
  present: boolean
}) {
  return (
    <Section
      icon={verdict?.pass ? CircleCheck : CircleX}
      title="Verify verdict"
    >
      {!present || !verdict ? (
        <Empty>Not verified yet — no verdict recorded for this run.</Empty>
      ) : (
        <div className="flex flex-col gap-3 rounded-lg border bg-card p-4">
          <div className="flex flex-wrap items-center gap-3">
            <Badge
              variant="outline"
              className={cn(
                verdict.pass
                  ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                  : 'border-destructive/40 bg-destructive/10 text-destructive',
              )}
            >
              {verdict.pass ? (
                <CircleCheck className="size-3" />
              ) : (
                <CircleX className="size-3" />
              )}
              {verdict.pass ? 'passed' : 'failed'}
            </Badge>
            {verdict.summary && <p className="text-sm">{verdict.summary}</p>}
          </div>

          {verdict.failures && verdict.failures.length > 0 && (
            <ul className="flex flex-col gap-1 border-l-2 border-destructive/40 pl-3">
              {verdict.failures.map((f, i) => (
                <li key={i} className="text-sm text-destructive">
                  {f}
                </li>
              ))}
            </ul>
          )}

          {verdict.checks && verdict.checks.length > 0 && (
            <div className="flex flex-col gap-1.5 border-t pt-3">
              {verdict.checks.map((c) => (
                <div key={c.name} className="flex items-center gap-2 text-sm">
                  {c.pass ? (
                    <CircleCheck className="size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
                  ) : (
                    <CircleX className="size-4 shrink-0 text-destructive" />
                  )}
                  <span className="font-mono text-xs">{c.name}</span>
                  {c.detail && (
                    <span className="text-muted-foreground">— {c.detail}</span>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Section>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed px-4 py-6 text-sm text-muted-foreground">
      {children}
    </div>
  )
}
