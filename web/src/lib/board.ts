import type { RunState } from '@/components/trau/status-pill'
import { phasePill, phaseRank } from '@/lib/overview'
import { phaseLabel } from '@/lib/runlive'
import type { Run } from '@/lib/runs'

export function boardPill(
  run: Pick<Run, 'phase' | 'failure_class'>,
): { state: RunState; label: string } {
  switch (run.failure_class) {
    case 'paused':
      return { state: 'warn', label: 'paused' }
    case 'faulted':
      return { state: 'fail', label: 'fault' }
    case 'gave_up':
      return { state: 'fail', label: 'quarantined' }
    default:
      return phasePill(run.phase)
  }
}

export interface BoardColumn {
  key: string
  label: string
  runs: Run[]
}

const PIPELINE: { key: string; label: string; phases: string[] }[] = [
  { key: 'queued', label: 'queued', phases: [''] },
  { key: 'build', label: 'build', phases: ['building', 'built'] },
  { key: 'handoff', label: 'handoff', phases: ['handed_off'] },
  { key: 'verify', label: 'verify', phases: ['verified'] },
  { key: 'pr', label: 'pr', phases: ['pr_open'] },
  { key: 'merged', label: 'merged', phases: ['merged'] },
]

function byUpdatedDesc(a: Run, b: Run): number {
  return (b.updated_at ?? '').localeCompare(a.updated_at ?? '')
}

// boardColumns groups runs into the fixed pipeline phases the board draws as
// columns, with the most recent merged runs first. Any phase outside the
// pipeline (quarantined, a transient state) becomes a trailing column ordered by
// pipeline rank so the board never silently drops a run.
export function boardColumns(runs: Run[]): BoardColumn[] {
  const known = new Set(PIPELINE.flatMap((p) => p.phases))

  const columns: BoardColumn[] = PIPELINE.map((p) => {
    const bucket = runs.filter((r) => p.phases.includes(r.phase))
    return {
      key: p.key,
      label: p.label,
      runs: p.key === 'merged' ? [...bucket].sort(byUpdatedDesc) : bucket,
    }
  })

  const extras: string[] = []
  for (const run of runs) {
    if (!known.has(run.phase) && !extras.includes(run.phase)) extras.push(run.phase)
  }
  extras.sort((a, b) => phaseRank(a) - phaseRank(b) || a.localeCompare(b))
  for (const phase of extras) {
    columns.push({
      key: phase,
      label: phaseLabel(phase),
      runs: runs.filter((r) => r.phase === phase),
    })
  }

  return columns
}
