import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, createFileRoute } from '@tanstack/react-router'

import {
  Eyebrow,
  NoticeBanner,
  RunActionsMenu,
  StatusPill,
  TerminalCard,
  useActiveRepo,
  type CheckpointNotice,
} from '@/components/trau'
import { boardColumns, boardPill, type BoardColumn } from '@/lib/board'
import { reposQueryOptions, runsQueryOptions, type Run } from '@/lib/runs'

export const Route = createFileRoute('/runs')({
  component: Runs,
  loader: ({ context }) => context.queryClient.ensureQueryData(reposQueryOptions),
})

function Runs() {
  const { repo: active, repos } = useActiveRepo()
  const [notice, setNotice] = useState<CheckpointNotice | null>(null)

  const live = repos.find((r) => r.name === active)?.live ?? false

  return (
    <div className="flex flex-col gap-8">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="flex flex-col gap-2">
          <Eyebrow glyph="partial" className="text-info">
            RUNS
          </Eyebrow>
          <h1 className="text-balance text-2xl font-semibold tracking-tight text-foreground">
            Runs
          </h1>
          <p className="text-pretty text-sm leading-relaxed text-muted-foreground">
            Every tracked run, grouped by pipeline phase.
          </p>
        </div>
        {live && <StatusPill state="active" label="live" className="mb-1.5" />}
      </header>

      {repos.length === 0 && (
        <TerminalCard title="runs">
          <p className="font-sans text-sm leading-relaxed text-muted-foreground">
            No repos yet. Runs appear here once a trau loop runs in a repo on this machine.
          </p>
        </TerminalCard>
      )}

      {notice && <NoticeBanner notice={notice} onDismiss={() => setNotice(null)} />}

      {active && <Board repo={active} onNotice={setNotice} />}
    </div>
  )
}

function Board({
  repo,
  onNotice,
}: {
  repo: string
  onNotice: (notice: CheckpointNotice) => void
}) {
  const { data, error, isPending } = useQuery(runsQueryOptions(repo))
  const runs = data?.runs ?? []

  if (error) return <p className="font-mono text-sm text-destructive">{String(error)}</p>
  if (isPending) return <p className="font-mono text-sm text-muted-foreground">Loading…</p>
  if (runs.length === 0) {
    return (
      <p className="font-mono text-sm text-muted-foreground">
        No runs recorded for {repo} yet.
      </p>
    )
  }

  return (
    <div className="-mx-6 overflow-x-auto px-6 pb-4">
      <div className="flex min-w-max items-stretch gap-4">
        {boardColumns(runs).map((column) => (
          <Column key={column.key} repo={repo} column={column} onNotice={onNotice} />
        ))}
      </div>
    </div>
  )
}

const MERGED_LIMIT = 4

function Column({
  repo,
  column,
  onNotice,
}: {
  repo: string
  column: BoardColumn
  onNotice: (notice: CheckpointNotice) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const capped = column.key === 'merged' && !expanded && column.runs.length > MERGED_LIMIT
  const shown = capped ? column.runs.slice(0, MERGED_LIMIT) : column.runs
  const hidden = column.runs.length - shown.length

  return (
    <section className="flex w-72 shrink-0 flex-col gap-3">
      <span className="px-1 font-mono text-xs uppercase tracking-[0.18em] text-muted-foreground">
        {column.label} ({column.runs.length})
      </span>
      <div className="flex flex-1 flex-col gap-3 rounded-lg border border-border/60 bg-card/40 p-2.5">
        {column.runs.length === 0 ? (
          <div className="flex flex-1 items-center justify-center rounded-md border border-dashed border-border/60 px-3 py-8">
            <span className="font-mono text-xs text-faint">empty</span>
          </div>
        ) : (
          <>
            {shown.map((run) => (
              <RunCard key={run.ticket} repo={repo} run={run} onNotice={onNotice} />
            ))}
            {hidden > 0 && (
              <button
                type="button"
                onClick={() => setExpanded(true)}
                className="rounded-md border border-dashed border-border/60 px-3 py-2 font-mono text-xs text-muted-foreground hover:border-ring/40 hover:text-foreground"
              >
                + {hidden} more merged
              </button>
            )}
          </>
        )}
      </div>
    </section>
  )
}

function RunCard({
  repo,
  run,
  onNotice,
}: {
  repo: string
  run: Run
  onNotice: (notice: CheckpointNotice) => void
}) {
  const pill = boardPill(run)
  return (
    <Link
      to="/runs/$repo/$ticket"
      params={{ repo, ticket: run.ticket }}
      className="group flex flex-col gap-2.5 rounded-md border border-border bg-secondary/20 p-3 transition-colors hover:border-ring/40 hover:bg-secondary/40"
    >
      <div className="flex items-start justify-between gap-2">
        <span className="font-mono text-sm text-primary">{run.ticket}</span>
        <div onClick={(e) => e.preventDefault()}>
          <RunActionsMenu
            repo={repo}
            ticket={run.ticket}
            phase={run.phase}
            onNotice={onNotice}
          />
        </div>
      </div>
      {run.title && (
        <p className="text-pretty font-sans text-sm leading-relaxed text-foreground">
          {run.title}
        </p>
      )}
      {run.failure_reason && (
        <p className="text-pretty font-mono text-[0.7rem] leading-relaxed text-muted-foreground">
          {run.failure_reason}
        </p>
      )}
      <div className="pt-0.5">
        <StatusPill state={pill.state} label={pill.label} />
      </div>
    </Link>
  )
}
