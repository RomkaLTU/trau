import { Link, useNavigate } from '@tanstack/react-router'
import { Eye, Play, Plus, RefreshCw } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useActiveRepo } from '@/components/trau/active-repo'
import { EmptyState } from '@/components/trau/empty-state'
import { Eyebrow } from '@/components/trau/eyebrow'
import {
  ATTENTION_META,
  MetaInline,
  Panel,
  PhaseStepper,
  RepoFocus,
  StopButton,
  elapsed,
  useNow,
} from '@/components/trau/overview'
import { StatusPill } from '@/components/trau/status-pill'
import type { AttentionRun } from '@/lib/attention'
import {
  activeLoopCount,
  attentionPill,
  isActiveState,
  loopCardView,
  useRepoActivity,
  type LiveLoop,
  type RepoActivity,
} from '@/lib/overview'

function BoardLoopActivity({ loop, now }: { loop: LiveLoop; now: number }) {
  const view = loopCardView(loop.sessionState, {
    phase: loop.phase,
    failureClass: loop.failureClass,
  })
  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <StatusPill state={view.pill.state} label={view.pill.label} />
        {loop.ticket ? (
          <Link
            to="/live/$repo/$ticket"
            params={{ repo: loop.repo, ticket: loop.ticket }}
            className="font-mono text-sm text-primary hover:underline"
          >
            {loop.ticket}
          </Link>
        ) : null}
        {loop.title ? (
          <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
            {loop.title}
          </p>
        ) : view.copy ? (
          <span className="font-sans text-sm text-muted-foreground">
            {view.copy}
          </span>
        ) : null}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        {view.showStepper ? <PhaseStepper phase={loop.phase} /> : null}
        <MetaInline label="elapsed" value={elapsed(loop.startedAt, now)} />
      </div>
    </div>
  )
}

function BoardAttentionActivity({ item }: { item: AttentionRun }) {
  const failureClass = item.failure_class!
  const pill = attentionPill(failureClass)
  const action = ATTENTION_META[failureClass].action
  return (
    <div className="flex flex-wrap items-center gap-2">
      <StatusPill state={pill.state} label={pill.label} />
      <span className="font-mono text-sm text-primary">{item.ticket}</span>
      <p className="text-pretty font-sans text-sm leading-relaxed text-muted-foreground">
        {item.failure_reason || item.title || item.repo}
      </p>
      <Link
        to="/live/$repo/$ticket"
        params={{ repo: item.repo, ticket: item.ticket }}
        className="font-mono text-xs text-teal underline-offset-4 hover:underline"
      >
        {action} →
      </Link>
    </div>
  )
}

function IdleActivity() {
  return (
    <div className="flex items-center gap-2">
      <StatusPill state="todo" label="idle" />
      <span className="font-sans text-sm text-muted-foreground">
        No active work. Launch a run or start a loop.
      </span>
    </div>
  )
}

function boardRank(activity: RepoActivity): number {
  if (activeLoopCount(activity.loops) > 0) return 0
  if (activity.attention.length > 0) return 1
  return 2
}

function RepoStateDot({ activity }: { activity: RepoActivity }) {
  if (activeLoopCount(activity.loops) > 0) {
    return (
      <span aria-hidden="true" className="text-teal">
        ●
      </span>
    )
  }
  if (activity.attention.length > 0) {
    return (
      <span aria-hidden="true" className="text-warn">
        ⚠
      </span>
    )
  }
  return (
    <span aria-hidden="true" className="text-faint">
      ○
    </span>
  )
}

function RepoActions({
  activity,
  primary,
}: {
  activity: RepoActivity
  primary: LiveLoop | null
}) {
  const { setRepo } = useActiveRepo()
  const navigate = useNavigate()

  if (primary) {
    const view = loopCardView(primary.sessionState, {
      phase: primary.phase,
      failureClass: primary.failureClass,
    })
    return (
      <div className="flex items-center gap-2">
        {view.showWatch && primary.ticket ? (
          <Button asChild variant="outline" size="sm" className="font-mono">
            <Link
              to="/live/$repo/$ticket"
              params={{ repo: primary.repo, ticket: primary.ticket }}
            >
              <Eye data-icon="inline-start" aria-hidden="true" />
              View
            </Link>
          </Button>
        ) : null}
        {view.showStop ? (
          <StopButton
            pid={primary.pid}
            repo={primary.repo}
            disabled={view.stopDisabled}
          />
        ) : null}
      </div>
    )
  }

  const focus = (to: '/run-once' | '/loop') => {
    setRepo(activity.repo.name)
    void navigate({ to })
  }

  return (
    <div className="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        className="font-mono"
        onClick={() => focus('/run-once')}
      >
        <Play data-icon="inline-start" aria-hidden="true" />
        Run once
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="font-mono"
        onClick={() => focus('/loop')}
      >
        <RefreshCw data-icon="inline-start" aria-hidden="true" />
        Loop
      </Button>
    </div>
  )
}

function RepoRow({ activity, now }: { activity: RepoActivity; now: number }) {
  const primary =
    activity.loops.find((loop) => isActiveState(loop.sessionState)) ??
    activity.loops[0] ??
    null
  const liveTickets = new Set(
    activity.loops.map((loop) => loop.ticket).filter(Boolean),
  )
  const attention = activity.attention.filter(
    (item) => !liveTickets.has(item.ticket),
  )
  const idle = !primary && attention.length === 0

  return (
    <li className="flex flex-col gap-4 px-5 py-4 lg:grid lg:grid-cols-[13rem_1fr_auto] lg:items-start lg:gap-6">
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2 font-mono text-sm text-foreground">
          <RepoStateDot activity={activity} />
          {activity.repo.name}
        </div>
        <span className="truncate font-mono text-[0.65rem] text-muted-foreground">
          {activity.repo.root}
        </span>
        <span className="font-mono text-[0.65rem] text-faint">
          {activity.metered ? '' : '≥ '}${activity.spend.toFixed(2)} today
        </span>
      </div>

      <div className="flex min-w-0 flex-col gap-3">
        {primary ? <BoardLoopActivity loop={primary} now={now} /> : null}
        {attention.map((item) => (
          <BoardAttentionActivity key={item.ticket} item={item} />
        ))}
        {idle ? <IdleActivity /> : null}
      </div>

      <div className="lg:pt-0.5">
        <RepoActions activity={activity} primary={primary} />
      </div>
    </li>
  )
}

function RepoBoard() {
  const activity = useRepoActivity()
  const now = useNow(1000)
  const rows = [...activity].sort((a, b) => boardRank(a) - boardRank(b))

  return (
    <Panel title="repos" count={rows.length}>
      <ul className="flex flex-col divide-y divide-border/60">
        {rows.map((row) => (
          <RepoRow key={row.repo.name} activity={row} now={now} />
        ))}
      </ul>
    </Panel>
  )
}

export function OverviewBoard() {
  const { repo, repos, isAll } = useActiveRepo()

  if (repos.length === 0) {
    return (
      <EmptyState
        message="No repos yet. Register a repo to check it out and start driving loops from here."
        actions={
          <Button asChild size="sm" className="font-mono">
            <Link to="/instances">
              <Plus data-icon="inline-start" aria-hidden="true" />
              Add a repo
            </Link>
          </Button>
        }
      />
    )
  }

  if (isAll) {
    return (
      <div className="flex flex-col gap-2">
        <Eyebrow glyph="active">REPOS</Eyebrow>
        <RepoBoard />
      </div>
    )
  }

  if (!repo) {
    return (
      <EmptyState message="Pick a repo from the switcher to focus it, or choose All repos." />
    )
  }

  return <RepoFocus repo={repo} />
}
