// The canonical skip keys, mirroring internal/config/skips.go: each one names
// exactly one Activity of a Step that a single run may bypass (ADR 0037). The
// hub refuses anything else, so this list is the whole vocabulary.
export const SKIP_LINTFIX = 'lintfix'
export const SKIP_CLEANUP = 'cleanup'
export const SKIP_VERIFY = 'verify'
export const SKIP_CI = 'ci'
export const SKIP_MERGE = 'merge'

// SKIP_KEYS is the canonical order the hub stores a set in, so a set built here
// round-trips through the queue unchanged.
export const SKIP_KEYS: string[] = [
  SKIP_LINTFIX,
  SKIP_CLEANUP,
  SKIP_VERIFY,
  SKIP_CI,
  SKIP_MERGE,
]

export interface PipelineActivity {
  label: string
  // skip is the canonical key that bypasses this Activity. An Activity without
  // one is locked on: a run that produces nothing has nothing to skip.
  skip?: string
  caption?: string
}

export interface PipelineStep {
  label: string
  activities: PipelineActivity[]
}

// PIPELINE_STEPS is what a run does, as the step picker lists it: the Activities
// grouped under the three Steps the stepper already shows. Verify is one row
// because its key closes the whole Step at once.
export const PIPELINE_STEPS: PipelineStep[] = [
  {
    label: 'Build',
    activities: [
      { label: 'Build', caption: 'writes the slice' },
      { label: 'Lint-fix', skip: SKIP_LINTFIX },
      { label: 'Cleanup', skip: SKIP_CLEANUP },
    ],
  },
  {
    label: 'Verify',
    activities: [
      {
        label: 'Verify',
        skip: SKIP_VERIFY,
        caption:
          'covers handoff brief, test gate, AI verification, repairs, browser verify',
      },
    ],
  },
  {
    label: 'Ship',
    activities: [
      { label: 'Commit', caption: 'commits the work' },
      { label: 'PR', caption: 'opens the pull request' },
      { label: 'CI wait', skip: SKIP_CI },
      { label: 'Auto-merge', skip: SKIP_MERGE },
    ],
  },
]

// canonicalSkips renders a named set in the canonical order and drops the
// repeats, so the same choice always sends the same set.
export function canonicalSkips(keys: Iterable<string>): string[] {
  const named = new Set(keys)
  return SKIP_KEYS.filter((key) => named.has(key))
}

// toggleSkip settles one Activity's checkbox: ticked means the run does the
// work, so it leaves the skip set; unticked adds it.
export function toggleSkip(
  skips: string[],
  key: string,
  enabled: boolean,
): string[] {
  const named = new Set(skips)
  if (enabled) named.delete(key)
  else named.add(key)
  return canonicalSkips(named)
}
